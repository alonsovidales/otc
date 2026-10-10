// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// TokenKind says what a Token is.
type TokenKind int

const (
	// Literal is plain text, to be shown as it is (escaped as the output
	// format needs, never interpreted).
	Literal TokenKind = iota
	// Placeholder is "{name}": Value is the argument's name.
	Placeholder
	// OpenTag is "<name>" and CloseTag "</name>": Value is the tag name.
	OpenTag
	CloseTag
)

// Token is one piece of a form split by Tokenize.
type Token struct {
	Kind  TokenKind
	Value string
}

// Tokenize splits a form into literal text, placeholders and tags - the
// first step of the rendering contract in docs/i18n.md: a renderer works
// on these tokens and never searches text (or an inserted argument) for
// braces or angle brackets again.
//
// The grammar is strict so that every platform parses it the same way:
// "{" and "}" appear only as "{name}" around an argument name; "<" only
// opens "<name>" or "</name>" (no attributes, no entities, no spaces);
// tags are balanced and don't nest. A literal "<", "{" or "}" can't be
// written at all - say it in words. A stray ">" is plain text.
func Tokenize(s string) ([]Token, error) {
	var toks []Token
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			toks = append(toks, Token{Literal, lit.String()})
			lit.Reset()
		}
	}
	open := ""
	for i := 0; i < len(s); {
		switch s[i] {
		case '{':
			end := strings.IndexAny(s[i+1:], "{}")
			if end < 0 || s[i+1+end] != '}' {
				return nil, fmt.Errorf("%q opens a placeholder that never closes (write braces as {name} only)", clip(s[i:]))
			}
			name := s[i+1 : i+1+end]
			if !argNamePattern.MatchString(name) {
				return nil, fmt.Errorf("invalid placeholder %q (placeholders are {name} with a lowercase argument name)", s[i:i+2+end])
			}
			flush()
			toks = append(toks, Token{Placeholder, name})
			i += end + 2
		case '}':
			return nil, fmt.Errorf("stray %q (braces appear only around a placeholder)", "}")
		case '<':
			end := strings.IndexAny(s[i+1:], "<>")
			if end < 0 || s[i+1+end] != '>' {
				return nil, fmt.Errorf("%q: \"<\" only opens a tag such as <b> (write \"less than\" in words)", clip(s[i:]))
			}
			inner := s[i+1 : i+1+end]
			closing := strings.HasPrefix(inner, "/")
			name := strings.TrimPrefix(inner, "/")
			if !tagNamePattern.MatchString(name) {
				return nil, fmt.Errorf("invalid tag %q (tags are <name>...</name>, lowercase, with no attributes)", s[i:i+2+end])
			}
			flush()
			if closing {
				if open != name {
					if open == "" {
						return nil, fmt.Errorf("</%s> closes a tag that isn't open", name)
					}
					return nil, fmt.Errorf("</%s> where </%s> was expected", name, open)
				}
				open = ""
				toks = append(toks, Token{CloseTag, name})
			} else {
				if open != "" {
					return nil, fmt.Errorf("<%s> inside <%s> (tags don't nest)", name, open)
				}
				open = name
				toks = append(toks, Token{OpenTag, name})
			}
			i += end + 2
		default:
			lit.WriteByte(s[i])
			i++
		}
	}
	if open != "" {
		return nil, fmt.Errorf("<%s> is never closed", open)
	}
	flush()
	return toks, nil
}

// clip shortens s for an error message.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= 24 {
		return s
	}
	r := []rune(s)
	return string(r[:24]) + "…"
}

// Placeholders returns the argument names a form uses, in order of first
// use, or nil when it doesn't tokenize.
func Placeholders(s string) []string {
	toks, err := Tokenize(s)
	if err != nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, t := range toks {
		if t.Kind == Placeholder && !seen[t.Value] {
			seen[t.Value] = true
			names = append(names, t.Value)
		}
	}
	return names
}

// Tags returns the tag names a form opens, in order, repeats included, or
// nil when it doesn't tokenize.
func Tags(s string) []string {
	toks, err := Tokenize(s)
	if err != nil {
		return nil
	}
	var names []string
	for _, t := range toks {
		if t.Kind == OpenTag {
			names = append(names, t.Value)
		}
	}
	return names
}

// TextLength is a form's length as Entry.Max counts it: code points, tags
// left out, each placeholder counted as written ("{name}" is 6).
func TextLength(s string) int {
	toks, err := Tokenize(s)
	if err != nil {
		return utf8.RuneCountInString(s)
	}
	n := 0
	for _, t := range toks {
		switch t.Kind {
		case Literal:
			n += utf8.RuneCountInString(t.Value)
		case Placeholder:
			n += utf8.RuneCountInString(t.Value) + 2
		}
	}
	return n
}
