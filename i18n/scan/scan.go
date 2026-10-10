// SPDX-License-Identifier: AGPL-3.0-or-later

// Package scan finds user-visible text that is still written into the source
// instead of coming from the catalog (docs/i18n.md, "Generated files": the
// hard-coded-text scanners). Each surface (the web app, the iOS, macOS and
// Android apps, the three Go programs, the bridge's pages and the setup
// wizard) has its own scanner; the counts per file are compared with a
// committed baseline that may only go down (i18n/scan/baseline/).
//
// The scanners are heuristics tuned on this repository: they look for text
// where it reaches a person (a SwiftUI Text, a JSX text node, an error sent
// in a reply, a tray menu label) and, in code that is mostly interface, for
// any literal that reads like a sentence. A line that must keep its literal
// carries a trailing comment "i18n-ignore: <reason>".
package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one hard-coded text.
type Finding struct {
	Surface string
	File    string // repository-relative, slash-separated
	Line    int    // 1-based
	EndLine int    // last line of the literal (same as Line for most)
	Rule    string // why it counts: "jsx-text", "sink:Text", "prose", ...
	Text    string // the literal, interpolations shown as {}
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.File, f.Line, f.Rule, clip(f.Text, 100))
}

// Surface is one scanner.
type Surface struct {
	Name string
	Desc string
	scan func(s *scanner) error
}

// Surfaces lists every scanner, in the order they are reported.
func Surfaces() []Surface {
	return []Surface{
		{"web", "web app: TSX/TS through the TypeScript compiler (node and web/node_modules)", scanWeb},
		{"ios", "iOS app: Swift", scanIOS},
		{"macos", "macOS app: Swift", scanMacOS},
		{"android", "Android app: Kotlin", scanAndroid},
		{"device", "device: Go replies, alerts, pushes and the errors they carry", scanDevice},
		{"bridge", "bridge: Go API errors, emails, pushes and replies", scanBridge},
		{"otcsync", "otc-sync: Go tray, dialogs and the errors they show", scanOTCSync},
		{"pages", "bridge pages: landing, account, disabled and unavailable", scanPages},
		{"wizard", "setup wizard: its page and its backend's errors", scanWizard},
	}
}

// SurfaceNames lists the surface names.
func SurfaceNames() []string {
	var out []string
	for _, s := range Surfaces() {
		out = append(out, s.Name)
	}
	return out
}

// Options narrow a scan.
type Options struct {
	Root     string   // repository root
	Surfaces []string // empty: all
	Node     string   // node binary for the web surface (default: "node" on PATH)
	// TypeScript is the compiler's module directory for the web surface
	// (default: <Root>/web/node_modules/typescript).
	TypeScript string
}

// Result is the findings of the surfaces scanned.
type Result struct {
	Surfaces []string             // scanned, in report order
	Findings map[string][]Finding // surface -> findings, sorted by file and line
	Ignored  map[string]int       // surface -> lines excluded by i18n-ignore
	Files    map[string][]string  // surface -> every file scanned (relative)
}

// Counts returns file -> count for one surface.
func (r *Result) Counts(surface string) map[string]int {
	out := map[string]int{}
	for _, f := range r.Findings[surface] {
		out[f.File]++
	}
	return out
}

// Run scans the surfaces opts names (all by default).
func Run(opts Options) (*Result, error) {
	want := map[string]bool{}
	for _, n := range opts.Surfaces {
		want[n] = true
	}
	known := map[string]bool{}
	for _, s := range Surfaces() {
		known[s.Name] = true
	}
	for n := range want {
		if !known[n] {
			return nil, fmt.Errorf("unknown surface %q (have %s)", n, strings.Join(SurfaceNames(), ", "))
		}
	}
	never := loadNever(opts.Root)
	res := &Result{Findings: map[string][]Finding{}, Ignored: map[string]int{}, Files: map[string][]string{}}
	var errs []error
	for _, s := range Surfaces() {
		if len(want) > 0 && !want[s.Name] {
			continue
		}
		sc := &scanner{surface: s.Name, root: opts.Root, node: opts.Node, typescript: opts.TypeScript, never: never,
			lines: map[string][]string{}}
		if err := s.scan(sc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		// by file and line; on one line, in the order the scanner met them
		sort.SliceStable(sc.found, func(i, j int) bool {
			a, b := sc.found[i], sc.found[j]
			if a.File != b.File {
				return a.File < b.File
			}
			return a.Line < b.Line
		})
		sort.Strings(sc.files)
		res.Surfaces = append(res.Surfaces, s.Name)
		res.Findings[s.Name] = sc.found
		res.Ignored[s.Name] = sc.ignored
		res.Files[s.Name] = sc.files
	}
	return res, errors.Join(errs...)
}

// scanner collects one surface's findings.
type scanner struct {
	surface    string
	root       string
	node       string
	typescript string
	never      *neverTerms
	found      []Finding
	files      []string
	ignored    int
	lines      map[string][]string // file -> its lines, for the ignore marker
	gen        map[string][]bool   // file -> which lines are generated (absent: none)
}

// source reads a repository file and remembers its lines.
func (s *scanner) source(rel string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	s.files = append(s.files, rel)
	lines := strings.Split(string(data), "\n")
	s.lines[rel] = lines
	if strings.Contains(string(data), beginGenerated) {
		if s.gen == nil {
			s.gen = map[string][]bool{}
		}
		s.gen[rel] = generatedLines(lines)
	}
	return data, nil
}

// add records a finding unless one of its lines carries the ignore marker or
// sits in a generated block.
func (s *scanner) add(rel string, line, endLine int, rule, text string) {
	if endLine < line {
		endLine = line
	}
	lines := s.lines[rel]
	for l := line; l <= endLine && l <= len(lines); l++ {
		if l >= 1 && hasIgnore(lines[l-1]) {
			s.ignored++
			return
		}
	}
	if g := s.gen[rel]; line >= 1 && line <= len(g) && g[line-1] {
		return
	}
	s.found = append(s.found, Finding{Surface: s.surface, File: rel, Line: line, EndLine: endLine, Rule: rule, Text: oneLine(text)})
}

// walk lists the files under the repository-relative dirs whose names pass
// keep, skipping directories skip rejects, in lexical order.
func (s *scanner) walk(dirs []string, skipDir func(rel string) bool, keep func(rel string) bool) ([]string, error) {
	var out []string
	for _, dir := range dirs {
		base := filepath.Join(s.root, filepath.FromSlash(dir))
		if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(s.root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			name := d.Name()
			if d.IsDir() {
				if p != base && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" ||
					name == "build" || name == "DerivedData" || (skipDir != nil && skipDir(rel))) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasPrefix(name, ".") && keep(rel) {
				out = append(out, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// exists reports whether a repository-relative file exists.
func (s *scanner) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(s.root, filepath.FromSlash(rel)))
	return err == nil
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
