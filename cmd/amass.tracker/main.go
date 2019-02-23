// Copyright 2017 Jeff Foley. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/OWASP/Amass/amass"
	"github.com/OWASP/Amass/amass/core"
	"github.com/OWASP/Amass/amass/handlers"
	"github.com/fatih/color"
)

const (
	timeFormat string = "01/02 15:04:05 2006 MST"
)

// Types that implement the flag.Value interface for parsing
type parseStrings []string

var (
	// Colors used to ease the reading of program output
	y      = color.New(color.FgHiYellow)
	g      = color.New(color.FgHiGreen)
	r      = color.New(color.FgHiRed)
	b      = color.New(color.FgHiBlue)
	fgR    = color.New(color.FgRed)
	fgY    = color.New(color.FgYellow)
	yellow = color.New(color.FgHiYellow).SprintFunc()
	green  = color.New(color.FgHiGreen).SprintFunc()
	blue   = color.New(color.FgHiBlue).SprintFunc()
	// Command-line switches and provided parameters
	help     = flag.Bool("h", false, "Show the program usage message")
	list     = flag.Bool("list", false, "Print information for all available enumerations")
	vprint   = flag.Bool("version", false, "Print the version number of this Amass binary")
	dir      = flag.String("dir", "", "Path to the directory containing the graph database")
	all      = flag.Bool("all", false, "Include all enumerations in the tracking")
	last     = flag.Int("last", 2, "The number of recent enumerations to include in the tracking")
	startStr = flag.String("start", "", "Exclude all enumerations before (format: "+timeFormat+")")
)

func main() {
	var domains parseStrings

	defaultBuf := new(bytes.Buffer)
	flag.CommandLine.SetOutput(defaultBuf)
	flag.Usage = func() {
		printBanner()
		g.Fprintf(color.Error, "Usage: %s [options] -d domain\n\n", path.Base(os.Args[0]))
		flag.PrintDefaults()
		g.Fprintln(color.Error, defaultBuf.String())
		os.Exit(1)
	}
	flag.Var(&domains, "d", "Domain names separated by commas (can be used multiple times)")
	flag.Parse()

	// Some input validation
	if *help || len(os.Args) == 1 {
		flag.Usage()
	}
	if *vprint {
		fmt.Fprintf(color.Error, "version %s\n", amass.Version)
		return
	}
	if len(domains) == 0 {
		r.Fprintln(color.Error, "No root domain names were provided")
		return
	}
	if *startStr != "" && (*last != 2 || *all) {
		r.Fprintln(color.Error, "The start flag cannot be used with the last or all flags")
		return
	}

	var err error
	var start time.Time
	if *startStr != "" {
		start, err = time.Parse(timeFormat, *startStr)
		if err != nil {
			r.Fprintf(color.Error, "%s is not in the correct format: %s\n", *startStr, timeFormat)
			return
		}
	}

	rand.Seed(time.Now().UTC().UnixNano())
	// Check that the default graph database directory exists in the CWD
	if *dir == "" {
		if finfo, err := os.Stat(handlers.DefaultGraphDBDirectory); os.IsNotExist(err) || !finfo.IsDir() {
			r.Fprintln(color.Error, "Failed to open the graph database")
			return
		}
	} else if finfo, err := os.Stat(*dir); os.IsNotExist(err) || !finfo.IsDir() {
		r.Fprintln(color.Error, "Failed to open the graph database")
		return
	}

	graph := handlers.NewGraph(*dir)
	if graph == nil {
		r.Fprintln(color.Error, "Failed to open the graph database")
		return
	}

	var enums []string
	// Obtain the enumerations that include the provided domain
	for _, e := range graph.EnumerationList() {
		if enumContainsDomain(e, domains[0], graph) {
			enums = append(enums, e)
		}
	}

	// The minimum is 2 in order to perform tracking analysis
	if *last < 2 {
		*last = 2
	}

	var begin int
	enums, earliest, latest := orderedEnumsAndDateRanges(enums, graph)
	// Filter out enumerations that begin before the start date/time
	if *startStr != "" {
		for _, e := range earliest {
			if !e.Before(start) {
				break
			}
			begin++
		}
	} else { // Or the number of enumerations from the end of the timeline
		if len(enums) < *last {
			r.Fprintf(color.Error, "%d enumerations are not available\n", *last)
			return
		}
		if *all == false {
			begin = len(enums) - *last
		}
	}
	enums = enums[begin:]
	earliest = earliest[begin:]
	latest = latest[begin:]

	// Check if the user has requested the list of enumerations
	if *list {
		for i := range enums {
			g.Printf("%d) %s -> %s\n", i+1, earliest[i].Format(timeFormat), latest[i].Format(timeFormat))
		}
		return
	}

	var prev string
	for i, enum := range enums {
		if prev == "" {
			prev = enum
			continue
		}

		fmt.Fprintf(color.Output, "%s\t%s%s%s\n%s\t%s%s%s\n\n", blue("Between"),
			yellow(earliest[i-1].Format(timeFormat)), blue(" -> "), yellow(latest[i-1].Format(timeFormat)),
			blue("and"), yellow(earliest[i].Format(timeFormat)), blue(" -> "), yellow(latest[i].Format(timeFormat)))

		out1 := getEnumDataInScope(domains[0], prev, graph)
		out2 := getEnumDataInScope(domains[0], enum, graph)
		for _, d := range diffEnumOutput(domains[0], out1, out2) {
			fmt.Fprintln(color.Output, d)
		}
		prev = enum
	}
}

func getEnumDataInScope(domain, enum string, h handlers.DataHandler) []*core.Output {
	var out []*core.Output

	for _, o := range h.GetOutput(enum, true) {
		if strings.HasSuffix(o.Name, domain) {
			out = append(out, o)
		}
	}
	return out
}

func diffEnumOutput(domain string, eout1, eout2 []*core.Output) []string {
	emap1 := make(map[string]*core.Output)
	emap2 := make(map[string]*core.Output)

	for _, o := range eout1 {
		emap1[o.Name] = o
	}
	for _, o := range eout2 {
		emap2[o.Name] = o
	}

	handled := make(map[string]struct{})
	var diff []string
	for _, o := range eout1 {
		handled[o.Name] = struct{}{}

		if _, found := emap2[o.Name]; !found {
			diff = append(diff, fmt.Sprintf("%s%s %s", blue("Removed: "),
				green(o.Name), yellow(lineOfAddresses(o.Addresses))))
			continue
		}

		o2 := emap2[o.Name]
		if !compareAddresses(o.Addresses, o2.Addresses) {
			diff = append(diff, fmt.Sprintf("%s%s\n\t%s\t%s\n\t%s\t%s", blue("Moved: "),
				green(o.Name), blue(" from "), yellow(lineOfAddresses(o.Addresses)),
				blue(" to "), yellow(lineOfAddresses(o2.Addresses))))
		}
	}

	for _, o := range eout2 {
		if _, found := handled[o.Name]; found {
			continue
		}

		if _, found := emap1[o.Name]; !found {
			diff = append(diff, fmt.Sprintf("%s%s %s", blue("Found: "),
				green(o.Name), yellow(lineOfAddresses(o.Addresses))))
		}
	}
	return diff
}

func lineOfAddresses(addrs []core.AddressInfo) string {
	var line string

	for i, addr := range addrs {
		if i != 0 {
			line = line + ","
		}
		line = line + addr.Address.String()
	}
	return line
}

func compareAddresses(addr1, addr2 []core.AddressInfo) bool {
	for _, a1 := range addr1 {
		var found bool

		for _, a2 := range addr2 {
			if a1.Address.Equal(a2.Address) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func orderedEnumsAndDateRanges(enums []string, h handlers.DataHandler) ([]string, []time.Time, []time.Time) {
	sort.Slice(enums, func(i, j int) bool {
		var less bool

		e1, l1 := h.EnumerationDateRange(enums[i])
		e2, l2 := h.EnumerationDateRange(enums[j])
		if l2.After(l1) || e1.Before(e2) {
			less = true
		}
		return less
	})

	var earliest, latest []time.Time
	for _, enum := range enums {
		e, l := h.EnumerationDateRange(enum)

		earliest = append(earliest, e)
		latest = append(latest, l)
	}
	return enums, earliest, latest
}

func enumContainsDomain(enum, domain string, h handlers.DataHandler) bool {
	var found bool

	for _, d := range h.EnumerationDomains(enum) {
		if d == domain {
			found = true
			break
		}
	}
	return found
}

func printBanner() {
	rightmost := 76
	version := "Version " + amass.Version
	desc := "In-depth DNS Enumeration and Network Mapping"
	author := "Authored By " + amass.Author

	pad := func(num int) {
		for i := 0; i < num; i++ {
			fmt.Fprint(color.Error, " ")
		}
	}
	r.Fprintln(color.Error, amass.Banner)
	pad(rightmost - len(version))
	y.Fprintln(color.Error, version)
	pad(rightmost - len(author))
	y.Fprintln(color.Error, author)
	pad(rightmost - len(desc))
	y.Fprintf(color.Error, "%s\n\n\n", desc)
}

// parseStrings implementation of the flag.Value interface
func (p *parseStrings) String() string {
	if p == nil {
		return ""
	}
	return strings.Join(*p, ",")
}

func (p *parseStrings) Set(s string) error {
	if s == "" {
		return fmt.Errorf("String parsing failed")
	}

	str := strings.Split(s, ",")
	for _, s := range str {
		*p = append(*p, strings.TrimSpace(s))
	}
	return nil
}
