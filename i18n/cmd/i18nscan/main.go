// SPDX-License-Identifier: AGPL-3.0-or-later

// Command i18nscan counts the user-visible text still written into the
// source, per file, and fails when a file has more than its committed
// baseline (docs/i18n.md, "Generated files"; i18n/scan/README.md says what
// counts). Counts only ever go down: a unit that moves text into the catalog
// lowers them with -update.
//
//	go run ./i18n/cmd/i18nscan                        # check every surface
//	go run ./i18n/cmd/i18nscan -surface web -v        # one surface, every finding
//	go run ./i18n/cmd/i18nscan -path web/src/App.tsx  # only these files
//	go run ./i18n/cmd/i18nscan -update -surface ios   # write the lower counts
//
// Flags:
//
//	-update          write the baseline from the current counts (with -surface
//	                 and -path, only those surfaces' and files' entries). A count
//	                 that rose is refused unless -allow-increase is given too
//	-allow-increase  with -update: also write counts that rose (a file split or
//	                 moved, a new scanner rule); say why in the commit
//	-surface a,b     only these surfaces: web, ios, macos, android, device,
//	                 bridge, otcsync, pages, wizard
//	-path p,q        only files under these repository-relative paths
//	-v               also print every finding and every file's count
//	-root dir        the repository root (default: found from the working directory)
//	-node path       the node binary for the web surface (default: node on PATH)
//	-typescript dir  the TypeScript module for the web surface (default:
//	                 web/node_modules/typescript under the root)
//
// A line that has to keep its literal carries a trailing comment
// "i18n-ignore: <reason>". It exits 0 when no count rose, 1 when one did (or
// -update refused), 2 on a usage error or a surface that could not be
// scanned (the web surface needs node and web/node_modules).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alonsovidales/otc/i18n/model"
	"github.com/alonsovidales/otc/i18n/scan"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("i18nscan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		update, allowIncrease, verbose bool
		surfaces, paths, root, node    string
		typescript                     string
	)
	fs.BoolVar(&update, "update", false, "write the baseline from the current counts")
	fs.BoolVar(&allowIncrease, "allow-increase", false, "with -update: also write counts that rose")
	fs.BoolVar(&verbose, "v", false, "print every finding and every file's count")
	fs.StringVar(&surfaces, "surface", "", "comma-separated surfaces ("+strings.Join(scan.SurfaceNames(), ", ")+")")
	fs.StringVar(&paths, "path", "", "comma-separated repository-relative paths to limit the check or update to")
	fs.StringVar(&root, "root", "", "repository root (default: found from the working directory)")
	fs.StringVar(&node, "node", "", "node binary for the web surface (default: node on PATH)")
	fs.StringVar(&typescript, "typescript", "", "TypeScript module directory for the web surface (default: web/node_modules/typescript)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "i18nscan: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if allowIncrease && !update {
		fmt.Fprintln(stderr, "i18nscan: -allow-increase only goes with -update")
		return 2
	}
	if root == "" {
		r, err := findRoot()
		if err != nil {
			fmt.Fprintf(stderr, "i18nscan: %v\n", err)
			return 2
		}
		root = r
	}
	res, scanErr := scan.Run(scan.Options{Root: root, Surfaces: splitList(surfaces), Node: node, TypeScript: typescript})
	if res == nil {
		fmt.Fprintf(stderr, "i18nscan: %v\n", scanErr)
		return 2
	}
	keep := scan.PathFilter(splitList(paths))

	type surfaceState struct {
		name     string
		base     *scan.Baseline
		exists   bool
		counts   map[string]int
		cmp      scan.Comparison
		findings []scan.Finding
	}
	var states []surfaceState
	for _, name := range res.Surfaces {
		b, ok, err := scan.LoadBaseline(root, name)
		if err != nil {
			fmt.Fprintf(stderr, "i18nscan: %v\n", err)
			return 2
		}
		counts := res.Counts(name)
		states = append(states, surfaceState{name, b, ok, counts, scan.Compare(b, counts, keep), res.Findings[name]})
	}

	// the report
	rose := 0
	for _, st := range states {
		total, files, baseTotal := 0, 0, 0
		for f, n := range st.counts {
			if keep(f) {
				total += n
				files++
			}
		}
		for f, n := range st.base.Files {
			if keep(f) {
				baseTotal += n
			}
		}
		base := fmt.Sprintf("baseline %d", baseTotal)
		if !st.exists {
			base = "no baseline yet"
		}
		ignored := ""
		if n := res.Ignored[st.name]; n > 0 {
			ignored = fmt.Sprintf(", %d ignored", n)
		}
		fmt.Fprintf(stdout, "%-8s %5d in %3d files (%s%s)\n", st.name, total, files, base, ignored)
		if verbose {
			var names []string
			for f := range st.counts {
				if keep(f) {
					names = append(names, f)
				}
			}
			sort.Strings(names)
			for _, f := range names {
				fmt.Fprintf(stdout, "  %5d %s\n", st.counts[f], f)
			}
			for _, fd := range st.findings {
				if keep(fd.File) {
					fmt.Fprintf(stdout, "    %s\n", fd)
				}
			}
		}
		if !update && st.exists {
			for _, c := range st.cmp.Rose {
				rose++
				fmt.Fprintf(stderr, "%s: %d hard-coded texts, baseline %d (+%d)\n", c.File, c.Now, c.Was, c.Now-c.Was)
				for _, fd := range st.findings {
					if fd.File == c.File {
						fmt.Fprintf(stderr, "  %s\n", fd)
					}
				}
			}
		}
	}
	if scanErr != nil {
		fmt.Fprintf(stderr, "i18nscan: %v\n", scanErr)
	}

	if update {
		if scanErr != nil {
			fmt.Fprintln(stderr, "i18nscan: nothing written: a surface could not be scanned")
			return 2
		}
		refused := 0
		for _, st := range states {
			if !st.exists || allowIncrease {
				continue
			}
			for _, c := range st.cmp.Rose {
				refused++
				fmt.Fprintf(stderr, "%s: %s: %d hard-coded texts, baseline %d: refusing to raise it\n", st.name, c.File, c.Now, c.Was)
			}
		}
		if refused > 0 {
			fmt.Fprintln(stderr, "i18nscan: nothing written: counts only go down - move the new text to the catalog, mark a line \"i18n-ignore: <reason>\", or pass -allow-increase (a file split or moved) and say why in the commit")
			return 1
		}
		for _, st := range states {
			k := keep
			if !st.exists {
				k = scan.PathFilter(nil) // a first baseline covers the whole surface, whatever -path says
			}
			nb := scan.Updated(st.base, st.counts, k)
			if st.exists && string(nb.Encode()) == string(st.base.Encode()) {
				continue
			}
			if err := nb.Write(root); err != nil {
				fmt.Fprintf(stderr, "i18nscan: %v\n", err)
				return 2
			}
			fmt.Fprintf(stdout, "wrote %s\n", scan.BaselineFile(st.name))
		}
		return 0
	}

	for _, st := range states {
		if !st.exists {
			fmt.Fprintf(stderr, "i18nscan: %s has no baseline (%s): write it with -update -surface %s\n", st.name, scan.BaselineFile(st.name), st.name)
			rose++
			continue
		}
		if n := len(st.cmp.Fell); n > 0 {
			fmt.Fprintf(stdout, "%s: %d %s below the baseline: lock it in with go run ./i18n/cmd/i18nscan -update -surface %s\n",
				st.name, n, plural(n, "file is", "files are"), st.name)
		}
	}
	if scanErr != nil {
		return 2
	}
	if rose > 0 {
		fmt.Fprintln(stderr, "i18nscan: hard-coded text grew: move it to the catalog (i18n/README.md, \"Adding or changing text\"), or mark a line that must keep its literal with a trailing comment \"i18n-ignore: <reason>\" (i18n/scan/README.md)")
		return 1
	}
	return 0
}

func plural(n int, one, other string) string {
	if n == 1 {
		return one
	}
	return other
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// findRoot walks up from the working directory to the directory holding
// i18n/languages.json.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(model.LanguagesFile))); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no " + model.LanguagesFile + " here or above; run from the repository or pass -root")
		}
		dir = parent
	}
}
