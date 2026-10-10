// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A small lexer for Swift and Kotlin: enough to find every string literal
// with its line, skip comments, and see the identifiers and punctuation
// around it. Interpolated code ("\(x)", "${x}", "$x") is skipped and shown
// as {} in the literal's text.

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tPunct
	tNumber
)

type ctok struct {
	kind    tokKind
	text    string // identifier, punctuation, or the literal's text with {} holes
	line    int
	endLine int
}

type cDialect int

const (
	dSwift cDialect = iota
	dKotlin
)

// puncts are the multi-character operators that matter here, longest first.
var puncts = []string{"===", "!==", "..<", "...", "==", "!=", "<=", ">=", "&&", "||", "??", "?:", "?.", "!!",
	"->", "=>", "+=", "-=", "*=", "/=", "::", ".."}

type clexer struct {
	src     string
	pos     int
	line    int
	dialect cDialect
	toks    []ctok
	bad     string // the first unterminated literal or comment, if any
}

func lexC(src string, d cDialect) []ctok {
	toks, _ := lexCChecked(src, d)
	return toks
}

// lexCChecked also says what was left open at the end of the source ("" when
// nothing): a file in the middle of an edit.
func lexCChecked(src string, d cDialect) ([]ctok, string) {
	lx := &clexer{src: src, line: 1, dialect: d}
	lx.run()
	return lx.toks, lx.bad
}

func (lx *clexer) unterminated(what string, line int) {
	if lx.bad == "" {
		lx.bad = fmt.Sprintf("an unterminated %s at line %d", what, line)
	}
}

func (lx *clexer) peek(off int) byte {
	if lx.pos+off < len(lx.src) {
		return lx.src[lx.pos+off]
	}
	return 0
}

func (lx *clexer) advance(n int) {
	for i := 0; i < n && lx.pos < len(lx.src); i++ {
		if lx.src[lx.pos] == '\n' {
			lx.line++
		}
		lx.pos++
	}
}

func (lx *clexer) run() {
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == '\n' || c == ' ' || c == '\t' || c == '\r':
			lx.advance(1)
		case c == '/' && lx.peek(1) == '/':
			for lx.pos < len(lx.src) && lx.src[lx.pos] != '\n' {
				lx.pos++
			}
		case c == '/' && lx.peek(1) == '*':
			lx.blockComment()
		case c == '"':
			lx.str(0)
		case c == '#' && lx.dialect == dSwift && lx.rawStart():
			n := 0
			for lx.peek(n) == '#' {
				n++
			}
			lx.advance(n)
			lx.str(n)
		case c == '\'' && lx.dialect == dKotlin:
			lx.char()
		case c == '`':
			// Kotlin and Swift backquoted identifiers
			start := lx.pos
			lx.advance(1)
			for lx.pos < len(lx.src) && lx.src[lx.pos] != '`' && lx.src[lx.pos] != '\n' {
				lx.pos++
			}
			lx.advance(1)
			lx.toks = append(lx.toks, ctok{kind: tIdent, text: strings.Trim(lx.src[start:lx.pos], "`"), line: lx.line, endLine: lx.line})
		case c >= '0' && c <= '9':
			start := lx.pos
			for lx.pos < len(lx.src) {
				d := lx.src[lx.pos]
				if d == '.' && lx.peek(1) == '.' {
					break
				}
				if !(d == '_' || d == '.' || d >= '0' && d <= '9' || d >= 'a' && d <= 'z' || d >= 'A' && d <= 'Z') {
					break
				}
				lx.pos++
			}
			lx.toks = append(lx.toks, ctok{kind: tNumber, text: lx.src[start:lx.pos], line: lx.line, endLine: lx.line})
		case isIdentStart(lx.src[lx.pos:]):
			start := lx.pos
			for lx.pos < len(lx.src) {
				r, size := utf8.DecodeRuneInString(lx.src[lx.pos:])
				if !(r == '_' || r == '$' && lx.dialect == dKotlin || unicode.IsLetter(r) || unicode.IsDigit(r)) {
					break
				}
				lx.pos += size
			}
			lx.toks = append(lx.toks, ctok{kind: tIdent, text: lx.src[start:lx.pos], line: lx.line, endLine: lx.line})
		default:
			p := ""
			for _, cand := range puncts {
				if strings.HasPrefix(lx.src[lx.pos:], cand) {
					p = cand
					break
				}
			}
			if p == "" {
				_, size := utf8.DecodeRuneInString(lx.src[lx.pos:])
				p = lx.src[lx.pos : lx.pos+size]
			}
			lx.toks = append(lx.toks, ctok{kind: tPunct, text: p, line: lx.line, endLine: lx.line})
			lx.advance(len(p))
		}
	}
}

func isIdentStart(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r == '_' || unicode.IsLetter(r)
}

// rawStart reports whether a '#' run starts a Swift raw string (#"…"#).
func (lx *clexer) rawStart() bool {
	n := 0
	for lx.peek(n) == '#' {
		n++
	}
	return lx.peek(n) == '"'
}

// blockComment skips /* … */, nested as both languages allow.
func (lx *clexer) blockComment() {
	start := lx.line
	defer func() {
		if lx.pos >= len(lx.src) && !strings.HasSuffix(lx.src, "*/") {
			lx.unterminated("comment", start)
		}
	}()
	depth := 0
	for lx.pos < len(lx.src) {
		switch {
		case strings.HasPrefix(lx.src[lx.pos:], "/*"):
			depth++
			lx.advance(2)
		case strings.HasPrefix(lx.src[lx.pos:], "*/"):
			depth--
			lx.advance(2)
			if depth == 0 {
				return
			}
		default:
			lx.advance(1)
		}
	}
}

// char skips a Kotlin character literal.
func (lx *clexer) char() {
	start := lx.line
	lx.advance(1)
	for lx.pos < len(lx.src) && lx.src[lx.pos] != '\'' && lx.src[lx.pos] != '\n' {
		if lx.src[lx.pos] == '\\' {
			lx.advance(1)
		}
		lx.advance(1)
	}
	lx.advance(1)
	lx.toks = append(lx.toks, ctok{kind: tNumber, text: "'c'", line: start, endLine: lx.line})
}

// str lexes a string literal starting at a quote, with hashes raw-string
// hashes (Swift) already consumed.
func (lx *clexer) str(hashes int) {
	start := lx.line
	multi := strings.HasPrefix(lx.src[lx.pos:], `"""`)
	if multi {
		lx.advance(3)
	} else {
		lx.advance(1)
	}
	closer := `"` + strings.Repeat("#", hashes)
	if multi {
		closer = `"""` + strings.Repeat("#", hashes)
	}
	escape := "\\" + strings.Repeat("#", hashes)
	var b strings.Builder
	for lx.pos < len(lx.src) {
		rest := lx.src[lx.pos:]
		switch {
		case strings.HasPrefix(rest, closer):
			if multi && lx.dialect == dKotlin {
				// """" ends with the last three quotes of the run
				for strings.HasPrefix(lx.src[lx.pos+1:], `"""`) {
					b.WriteByte('"')
					lx.advance(1)
				}
			}
			lx.advance(len(closer))
			lx.toks = append(lx.toks, ctok{kind: tString, text: b.String(), line: start, endLine: lx.line})
			return
		case !multi && rest[0] == '\n':
			// unterminated: give up on this literal at the end of the line
			lx.unterminated("string", start)
			lx.toks = append(lx.toks, ctok{kind: tString, text: b.String(), line: start, endLine: lx.line})
			return
		case lx.dialect == dSwift && strings.HasPrefix(rest, escape+"("):
			lx.advance(len(escape) + 1)
			lx.skipCode(')')
			b.WriteString(hole)
		case lx.dialect == dSwift && strings.HasPrefix(rest, escape):
			lx.advance(len(escape))
			b.WriteString(lx.escaped())
		case lx.dialect == dKotlin && !multi && rest[0] == '\\':
			lx.advance(1)
			b.WriteString(lx.escaped())
		case lx.dialect == dKotlin && strings.HasPrefix(rest, "${"):
			lx.advance(2)
			lx.skipCode('}')
			b.WriteString(hole)
		case lx.dialect == dKotlin && rest[0] == '$' && len(rest) > 1 && isIdentStart(rest[1:]):
			lx.advance(1)
			for lx.pos < len(lx.src) {
				r, size := utf8.DecodeRuneInString(lx.src[lx.pos:])
				if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
					break
				}
				lx.pos += size
			}
			b.WriteString(hole)
		default:
			_, size := utf8.DecodeRuneInString(rest)
			b.WriteString(rest[:size])
			lx.advance(size)
		}
	}
	lx.unterminated("string", start)
	lx.toks = append(lx.toks, ctok{kind: tString, text: b.String(), line: start, endLine: lx.line})
}

// escaped reads the character after a backslash.
func (lx *clexer) escaped() string {
	if lx.pos >= len(lx.src) {
		return ""
	}
	c := lx.src[lx.pos]
	lx.advance(1)
	switch c {
	case 'n':
		return "\n"
	case 't':
		return "\t"
	case 'r':
		return ""
	case '0':
		return ""
	case 'u':
		// \u{1F600} (Swift) or A (Kotlin)
		if lx.peek(0) == '{' {
			for lx.pos < len(lx.src) && lx.src[lx.pos] != '}' {
				lx.advance(1)
			}
			lx.advance(1)
		} else {
			lx.advance(4)
		}
		return "·"
	case '\n':
		return "" // a line continuation in a multi-line literal
	}
	return string(c)
}

// skipCode skips interpolated code up to the closer matching an opener
// already consumed, through nested brackets, strings and comments.
func (lx *clexer) skipCode(closer byte) {
	depth := 0
	opener := byte('(')
	if closer == '}' {
		opener = '{'
	}
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == '"':
			// lex and drop: a nested literal is part of the interpolation
			n := len(lx.toks)
			lx.str(0)
			lx.toks = lx.toks[:n]
			continue
		case c == '\'' && lx.dialect == dKotlin:
			n := len(lx.toks)
			lx.char()
			lx.toks = lx.toks[:n]
			continue
		case c == '/' && lx.peek(1) == '*':
			lx.blockComment()
			continue
		case c == opener:
			depth++
		case c == closer:
			if depth == 0 {
				lx.advance(1)
				return
			}
			depth--
		}
		lx.advance(1)
	}
}
