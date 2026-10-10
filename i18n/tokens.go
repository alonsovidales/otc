// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import "strings"

// tokKind says what a token is.
type tokKind uint8

const (
	tokLit   tokKind = iota // plain text
	tokArg                  // {name}: val is the argument's name
	tokOpen                 // <name>: val is the tag
	tokClose                // </name>
)

type token struct {
	kind tokKind
	val  string
}

// tokenize splits a form into literal text, placeholders and tags with the
// grammar of i18n/model's Tokenize, which the generator already checked
// every form against: "{" and "}" appear only as "{name}" around an
// argument name, "<" only opens "<name>" or "</name>", and tags are
// balanced and don't nest. ok is false for anything else - the entry is
// then left out, never guessed at.
func tokenize(s string) (toks []token, ok bool) {
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			toks = append(toks, token{tokLit, lit.String()})
			lit.Reset()
		}
	}
	open := ""
	for i := 0; i < len(s); {
		switch s[i] {
		case '{':
			end := strings.IndexAny(s[i+1:], "{}")
			if end < 0 || s[i+1+end] != '}' || !validArgName(s[i+1:i+1+end]) {
				return nil, false
			}
			flush()
			toks = append(toks, token{tokArg, s[i+1 : i+1+end]})
			i += end + 2
		case '}':
			return nil, false
		case '<':
			end := strings.IndexAny(s[i+1:], "<>")
			if end < 0 || s[i+1+end] != '>' {
				return nil, false
			}
			inner := s[i+1 : i+1+end]
			name, closing := strings.CutPrefix(inner, "/")
			if !validTagName(name) {
				return nil, false
			}
			flush()
			if closing {
				if open != name {
					return nil, false
				}
				open = ""
				toks = append(toks, token{tokClose, name})
			} else {
				if open != "" {
					return nil, false
				}
				open = name
				toks = append(toks, token{tokOpen, name})
			}
			i += end + 2
		default:
			lit.WriteByte(s[i])
			i++
		}
	}
	if open != "" {
		return nil, false
	}
	flush()
	return toks, true
}

// validArgName matches ^[a-z][a-z0-9]*(_[a-z0-9]+)*$.
func validArgName(s string) bool {
	if s == "" || !isLower(s[0]) || s[len(s)-1] == '_' {
		return false
	}
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case isLower(c) || isDigit(c):
		case c == '_' && s[i-1] != '_':
		default:
			return false
		}
	}
	return true
}

// validTagName matches ^[a-z][a-z0-9]*$.
func validTagName(s string) bool {
	if s == "" || !isLower(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isLower(s[i]) && !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
