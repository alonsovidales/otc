// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Severity of a diagnostic.
type Severity int

const (
	Info Severity = iota
	Warning
	Error
)

func (s Severity) String() string {
	return [...]string{"info", "warning", "error"}[s]
}

// Class decides a diagnostic's severity under the check options.
type Class int

const (
	// ClassError is always an error.
	ClassError Class = iota
	// ClassPending is work not done yet: a missing or stale translation, or
	// a review-required text without a current review. Info for a draft
	// language, a warning for a shipping one, and an error under -release
	// for shipping languages and those named with -lang.
	ClassPending
	// ClassGlossary is a glossary term not followed: a warning, an error
	// under -strict.
	ClassGlossary
)

// Diagnostic is one problem found in the catalog.
type Diagnostic struct {
	Severity Severity
	Class    Class
	File     string // repository-relative, slash-separated
	Line     int    // 0 when it isn't about one line
	Key      string
	Lang     string // "" when not about one language
	Prefix   string // "" when not about one prefix
	Msg      string
}

// String formats a diagnostic the way compilers do, so editors can jump
// to it: "file:line: error: key: message".
func (d Diagnostic) String() string {
	var b strings.Builder
	if d.File != "" {
		b.WriteString(d.File)
		if d.Line > 0 {
			fmt.Fprintf(&b, ":%d", d.Line)
		}
		b.WriteString(": ")
	}
	b.WriteString(d.Severity.String())
	b.WriteString(": ")
	if d.Key != "" {
		b.WriteString(d.Key)
		b.WriteString(": ")
	}
	b.WriteString(d.Msg)
	return b.String()
}

// CheckOptions narrows and grades a check.
type CheckOptions struct {
	// Release makes pending work an error for shipping languages and for
	// every language in Langs.
	Release bool
	// Strict makes glossary findings errors.
	Strict bool
	// Langs, when set, keeps only diagnostics about these languages (and
	// those about no language or about English, which every language
	// depends on).
	Langs []string
	// Prefixes, when set, keeps only diagnostics about these prefixes; a
	// name also selects the prefixes under it ("app" selects "app.photos").
	Prefixes []string
}

// PrefixSelected reports whether prefix p is chosen by a -prefix list.
func PrefixSelected(p string, sel []string) bool {
	if len(sel) == 0 {
		return true
	}
	for _, s := range sel {
		if p == s || strings.HasPrefix(p, s+".") {
			return true
		}
	}
	return false
}

// grade sets each diagnostic's severity, drops what the filters leave out
// and sorts the rest by file, line and key.
func grade(ds []Diagnostic, opts CheckOptions, langs []Language) []Diagnostic {
	status := map[string]Status{}
	for _, l := range langs {
		status[l.Code] = l.Status
	}
	var out []Diagnostic
	for _, d := range ds {
		if len(opts.Langs) > 0 && d.Lang != "" && d.Lang != SourceCode && !slices.Contains(opts.Langs, d.Lang) {
			continue
		}
		if d.Prefix != "" && !PrefixSelected(d.Prefix, opts.Prefixes) {
			continue
		}
		switch d.Class {
		case ClassError:
			d.Severity = Error
		case ClassGlossary:
			d.Severity = Warning
			if opts.Strict {
				d.Severity = Error
			}
		case ClassPending:
			shipping := status[d.Lang] == Shipping
			switch {
			case opts.Release && (shipping || slices.Contains(opts.Langs, d.Lang)):
				d.Severity = Error
			case shipping:
				d.Severity = Warning
			default:
				d.Severity = Info
			}
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Msg < b.Msg
	})
	return out
}

// Count returns how many diagnostics have the given severity.
func Count(ds []Diagnostic, s Severity) int {
	n := 0
	for _, d := range ds {
		if d.Severity == s {
			n++
		}
	}
	return n
}
