// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"fmt"
	"slices"
	"sort"

	"golang.org/x/text/language"
)

// PseudoLanguage is the pseudo-locale -draft adds for local testing: every
// key's English with its letters accented, bracketed and padded (see
// PseudoText), so text that escaped the catalog stands out on screen and
// tight layouts show up. "qps" is a valid stored code (^[a-z]{2,3}$) no
// device has a catalog for, and en-XA is the pseudo-locale tag Android
// itself uses, with English plural rules everywhere.
var PseudoLanguage = Language{
	Code:    "qps",
	Tag:     "en-XA",
	Apple:   "en-XA",
	Android: "en-rXA",
	Name:    "Pseudo",
	Status:  Draft,
	Pseudo:  true,
}

// loadLanguages reads languages.json:
//
//	{"languages": [{"code", "tag", "apple", "android", "name", "status"}, ...],
//	 "routes": {"<root>": ["<consumer>", ...], ...}}
func loadLanguages(data []byte, file string) ([]Language, map[string][]string, []Diagnostic) {
	var ds []Diagnostic
	bad := func(line int, format string, a ...any) {
		ds = append(ds, Diagnostic{File: file, Line: line, Msg: fmt.Sprintf(format, a...)})
	}
	root, err := parseJSON(data)
	if err != nil {
		je := err.(*jsonError)
		bad(je.line, "%s", je.msg)
		return nil, nil, ds
	}
	if root.kind != kindObject {
		bad(root.line, "must be an object with \"languages\" and \"routes\"")
		return nil, nil, ds
	}
	for _, m := range root.obj {
		if m.key != "languages" && m.key != "routes" {
			bad(m.line, "unknown field %q", m.key)
		}
	}

	var langs []Language
	ln := root.field("languages")
	if ln == nil || ln.kind != kindArray {
		bad(root.line, "\"languages\" must be an array")
	} else {
		codes := map[string]bool{}
		for _, n := range ln.arr {
			if n.kind != kindObject {
				bad(n.line, "a language must be an object")
				continue
			}
			var l Language
			ok := true
			for _, m := range n.obj {
				if m.val.kind != kindString {
					bad(m.line, "%q must be a string", m.key)
					ok = false
					continue
				}
				v := m.val.str
				switch m.key {
				case "code":
					l.Code = v
				case "tag":
					l.Tag = v
				case "apple":
					l.Apple = v
				case "android":
					l.Android = v
				case "name":
					l.Name = v
				case "status":
					l.Status = Status(v)
				default:
					bad(m.line, "unknown field %q", m.key)
					ok = false
				}
			}
			if !ok {
				continue
			}
			switch {
			case !codePattern.MatchString(l.Code):
				bad(n.line, "code %q must be two or three lowercase letters", l.Code)
			case codes[l.Code]:
				bad(n.line, "code %q is listed twice", l.Code)
			case l.Code == PseudoLanguage.Code:
				bad(n.line, "code %q is reserved for the pseudo-locale", l.Code)
			}
			codes[l.Code] = true
			if t, err := language.Parse(l.Tag); err != nil || t.String() != l.Tag {
				bad(n.line, "%s: tag %q is not a canonical BCP 47 tag", l.Code, l.Tag)
			} else if base, _ := t.Base(); base.String() != l.Code {
				bad(n.line, "%s: tag %q is for another language", l.Code, l.Tag)
			}
			if l.Apple == "" || l.Name == "" {
				bad(n.line, "%s: \"apple\" and \"name\" are required", l.Code)
			}
			if (l.Android == "") != (l.Code == SourceCode) {
				bad(n.line, "%s: \"android\" is the values-<qualifier>, empty for English only", l.Code)
			}
			if l.Status != Shipping && l.Status != Draft {
				bad(n.line, "%s: status must be %q or %q", l.Code, Shipping, Draft)
			}
			langs = append(langs, l)
		}
		if len(langs) == 0 || langs[0].Code != SourceCode {
			bad(ln.line, "English (%q) must be the first language", SourceCode)
		} else if langs[0].Status != Shipping {
			bad(ln.line, "English must be %q", Shipping)
		}
	}

	routes := map[string][]string{}
	rn := root.field("routes")
	if rn == nil || rn.kind != kindObject {
		bad(root.line, "\"routes\" must be an object of root -> consumers")
	} else {
		for _, m := range rn.obj {
			if !rootPattern.MatchString(m.key) {
				bad(m.line, "route %q: a root is one lowercase segment", m.key)
				continue
			}
			if m.val.kind != kindArray || len(m.val.arr) == 0 {
				bad(m.line, "route %q must list its consumers", m.key)
				continue
			}
			var cs []string
			for _, c := range m.val.arr {
				switch {
				case c.kind != kindString || !slices.Contains(Consumers, c.str):
					bad(c.line, "route %q: unknown consumer (one of %v)", m.key, Consumers)
				case slices.Contains(cs, c.str):
					bad(c.line, "route %q lists %q twice", m.key, c.str)
				default:
					cs = append(cs, c.str)
				}
			}
			sort.Strings(cs)
			routes[m.key] = cs
		}
	}
	return langs, routes, ds
}
