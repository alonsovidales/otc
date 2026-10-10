// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The glossary files are written by the glossary unit and read here; the
// format is the contract between the two:
//
//	i18n/glossary/<code>.json:
//	  {"code": "es",
//	   "terms": [{"en": "Collections", "text": "Colecciones", "note": "...", "enforce": true}],
//	   "forbidden": [{"text": "álbum", "note": "we say colecciones"}]}
//
//	i18n/glossary/never.json:
//	  {"never": ["Off The Cloud", "Tailscale", ...]}
//
// A never-translated term is required, verbatim, in a translated form
// whenever the English form has it as a whole word (case matters: "Mac"
// counts in "a Mac app", not in "Machine"); in the translation it may be
// part of a longer word (German "des iPhones").

// Glossary is one language's terms.
type Glossary struct {
	Code      string
	Terms     []Term
	Forbidden []Forbidden
	File      string
}

// Term is a glossary term. With Enforce, whenever an English form has EN
// (case-insensitive, as a whole word) the translated form must contain
// Text (case-insensitive).
type Term struct {
	EN, Text, Note string
	Enforce        bool
	Line           int
}

// Forbidden is a word a translation must not use (case-insensitive, as a
// whole word).
type Forbidden struct {
	Text, Note string
	Line       int
}

// DefaultNever is used while i18n/glossary/never.json doesn't exist: the
// names docs/i18n.md lists as never translated.
var DefaultNever = []string{"Off The Cloud", "Tailscale", "GitHub", "Google", "Apple"}

func parseGlossary(data []byte, file, code string) (*Glossary, []Diagnostic) {
	var ds []Diagnostic
	bad := func(line int, format string, a ...any) {
		ds = append(ds, Diagnostic{File: file, Line: line, Lang: code, Msg: fmt.Sprintf(format, a...)})
	}
	root, err := parseJSON(data)
	if err != nil {
		je := err.(*jsonError)
		bad(je.line, "%s", je.msg)
		return nil, ds
	}
	if root.kind != kindObject {
		bad(root.line, "a glossary is an object with \"code\", \"terms\" and \"forbidden\"")
		return nil, ds
	}
	g := &Glossary{File: file}
	for _, m := range root.obj {
		switch m.key {
		case "code":
			if m.val.kind != kindString || m.val.str != code {
				bad(m.line, "\"code\" must be %q, the file's name", code)
			}
			g.Code = code
		case "terms", "forbidden":
			if m.val.kind != kindArray {
				bad(m.line, "%q must be an array", m.key)
				continue
			}
			for _, n := range m.val.arr {
				if n.kind != kindObject {
					bad(n.line, "each of %q is an object", m.key)
					continue
				}
				var t Term
				var f Forbidden
				for _, fm := range n.obj {
					switch {
					case fm.key == "enforce" && m.key == "terms":
						if fm.val.kind != kindBool {
							bad(fm.line, "\"enforce\" must be true or false")
						}
						t.Enforce = fm.val.b
					case fm.key == "note", fm.key == "text", fm.key == "en" && m.key == "terms":
						if fm.val.kind != kindString {
							bad(fm.line, "%q must be a string", fm.key)
							continue
						}
						switch fm.key {
						case "note":
							t.Note, f.Note = fm.val.str, fm.val.str
						case "text":
							t.Text, f.Text = fm.val.str, fm.val.str
						case "en":
							t.EN = fm.val.str
						}
					default:
						bad(fm.line, "unknown field %q", fm.key)
					}
				}
				if m.key == "terms" {
					t.Line = n.line
					if strings.TrimSpace(t.EN) == "" || strings.TrimSpace(t.Text) == "" {
						bad(n.line, "a term needs \"en\" and \"text\"")
						continue
					}
					g.Terms = append(g.Terms, t)
				} else {
					f.Line = n.line
					if strings.TrimSpace(f.Text) == "" {
						bad(n.line, "a forbidden entry needs \"text\"")
						continue
					}
					g.Forbidden = append(g.Forbidden, f)
				}
			}
		default:
			bad(m.line, "unknown field %q", m.key)
		}
	}
	if g.Code == "" {
		bad(root.line, "\"code\" is required")
	}
	return g, ds
}

func parseNever(data []byte, file string) ([]string, []Diagnostic) {
	var ds []Diagnostic
	bad := func(line int, format string, a ...any) {
		ds = append(ds, Diagnostic{File: file, Line: line, Msg: fmt.Sprintf(format, a...)})
	}
	root, err := parseJSON(data)
	if err != nil {
		je := err.(*jsonError)
		bad(je.line, "%s", je.msg)
		return nil, ds
	}
	if root.kind != kindObject {
		bad(root.line, "never.json is {\"never\": [\"term\", ...]}")
		return nil, ds
	}
	var never []string
	for _, m := range root.obj {
		if m.key != "never" {
			bad(m.line, "unknown field %q", m.key)
			continue
		}
		if m.val.kind != kindArray {
			bad(m.line, "\"never\" must be an array of strings")
			continue
		}
		for _, n := range m.val.arr {
			if n.kind != kindString || strings.TrimSpace(n.str) == "" {
				bad(n.line, "each never-translate term is a non-empty string")
				continue
			}
			never = append(never, n.str)
		}
	}
	return never, ds
}

// containsWordFold reports whether needle appears in s, ignoring case, with
// no letter or digit right before or after it.
func containsWordFold(s, needle string) bool {
	return containsWord(strings.ToLower(s), strings.ToLower(needle))
}

// containsWord reports whether needle appears in s, exactly, with no letter
// or digit right before or after it ("Mac" is in "a Mac app", not in
// "Machine").
func containsWord(s, needle string) bool {
	if needle == "" {
		return false
	}
	for off := 0; ; {
		i := strings.Index(s[off:], needle)
		if i < 0 {
			return false
		}
		start, end := off+i, off+i+len(needle)
		if !wordRuneBefore(s, start) && !wordRuneAfter(s, end) {
			return true
		}
		off = start + 1
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func wordRuneBefore(s string, i int) bool {
	if i == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return isWordRune(r)
}

func wordRuneAfter(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return isWordRune(r)
}

// containsFold reports whether needle appears in s, ignoring case.
func containsFold(s, needle string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(needle))
}
