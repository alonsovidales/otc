// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// A small JavaScript lexer for the scripts inside the bridge's pages and the
// wizard's page (the web app goes through the TypeScript compiler instead).
// It finds string and template literals with their lines and skips comments
// and regular expressions; ${…} in a template becomes {}.

type jsTok struct {
	kind    tokKind // tIdent, tString (also templates), tPunct, tNumber
	text    string
	line    int
	endLine int
	lines   []int // the source line of each byte of text
}

var jsPuncts = []string{"===", "!==", "**=", "...", "&&=", "||=", "??=", "=>", "==", "!=", "<=", ">=", "&&", "||",
	"??", "?.", "++", "--", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<", ">>"}

var jsRegexAfter = boolSet("return", "typeof", "case", "do", "else", "in", "of", "new", "delete", "void", "throw",
	"instanceof", "yield", "await")

type jslexer struct {
	src  string
	pos  int
	line int
	toks []jsTok
}

// lexJS lexes src, whose first byte is on line firstLine.
func lexJS(src string, firstLine int) []jsTok {
	lx := &jslexer{src: src, line: firstLine}
	lx.run()
	return lx.toks
}

func (lx *jslexer) advance(n int) {
	for i := 0; i < n && lx.pos < len(lx.src); i++ {
		if lx.src[lx.pos] == '\n' {
			lx.line++
		}
		lx.pos++
	}
}

// regexAllowed decides whether a slash starts a regular expression from the
// token before it. After ")" it is always a division, so "if (x) /re/" is
// misread; nothing in the pages writes that.
func (lx *jslexer) regexAllowed() bool {
	if len(lx.toks) == 0 {
		return true
	}
	t := lx.toks[len(lx.toks)-1]
	switch t.kind {
	case tString, tNumber:
		return false
	case tIdent:
		return jsRegexAfter[t.text]
	}
	return t.text != ")" && t.text != "]" && t.text != "}"
}

func (lx *jslexer) run() {
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		rest := lx.src[lx.pos:]
		switch {
		case c == '\n' || c == ' ' || c == '\t' || c == '\r':
			lx.advance(1)
		case strings.HasPrefix(rest, "//"):
			for lx.pos < len(lx.src) && lx.src[lx.pos] != '\n' {
				lx.pos++
			}
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest[2:], "*/")
			if end < 0 {
				lx.advance(len(rest))
			} else {
				lx.advance(end + 4)
			}
		case strings.HasPrefix(rest, "<!--"):
			// an HTML comment opener inside a script: a line comment
			for lx.pos < len(lx.src) && lx.src[lx.pos] != '\n' {
				lx.pos++
			}
		case c == '"' || c == '\'':
			lx.quoted(c)
		case c == '`':
			lx.template()
		case c == '/' && lx.regexAllowed():
			lx.regex()
		case c >= '0' && c <= '9':
			start := lx.pos
			for lx.pos < len(lx.src) && (isWordByte(lx.src[lx.pos]) || lx.src[lx.pos] == '.') {
				lx.pos++
			}
			lx.toks = append(lx.toks, jsTok{kind: tNumber, text: lx.src[start:lx.pos], line: lx.line, endLine: lx.line})
		case c == '_' || c == '$' || c >= 0x80 && unicode.IsLetter(firstRune(rest)) || c < 0x80 && unicode.IsLetter(rune(c)):
			start := lx.pos
			for lx.pos < len(lx.src) {
				r, size := utf8.DecodeRuneInString(lx.src[lx.pos:])
				if !(r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
					break
				}
				lx.pos += size
			}
			lx.toks = append(lx.toks, jsTok{kind: tIdent, text: lx.src[start:lx.pos], line: lx.line, endLine: lx.line})
		default:
			p := ""
			for _, cand := range jsPuncts {
				if strings.HasPrefix(rest, cand) {
					p = cand
					break
				}
			}
			if p == "" {
				_, size := utf8.DecodeRuneInString(rest)
				p = rest[:size]
			}
			lx.toks = append(lx.toks, jsTok{kind: tPunct, text: p, line: lx.line, endLine: lx.line})
			lx.advance(len(p))
		}
	}
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (lx *jslexer) quoted(q byte) {
	start := lx.line
	lx.advance(1)
	var b textBuilder
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == q:
			lx.advance(1)
			lx.toks = append(lx.toks, jsTok{kind: tString, text: b.String(), line: start, endLine: lx.line, lines: b.lines})
			return
		case c == '\n':
			lx.toks = append(lx.toks, jsTok{kind: tString, text: b.String(), line: start, endLine: lx.line, lines: b.lines})
			return
		case c == '\\':
			line := lx.line
			lx.advance(1)
			b.write(lx.escaped(), line)
		default:
			_, size := utf8.DecodeRuneInString(lx.src[lx.pos:])
			b.write(lx.src[lx.pos:lx.pos+size], lx.line)
			lx.advance(size)
		}
	}
	lx.toks = append(lx.toks, jsTok{kind: tString, text: b.String(), line: start, endLine: lx.line, lines: b.lines})
}

// textBuilder builds a literal's text and remembers each byte's line.
type textBuilder struct {
	strings.Builder
	lines []int
}

func (b *textBuilder) write(s string, line int) {
	b.WriteString(s)
	for range len(s) {
		b.lines = append(b.lines, line)
	}
}

func (lx *jslexer) escaped() string {
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
	case 'u', 'x':
		if lx.pos < len(lx.src) && lx.src[lx.pos] == '{' {
			for lx.pos < len(lx.src) && lx.src[lx.pos] != '}' {
				lx.advance(1)
			}
			lx.advance(1)
		} else if c == 'u' {
			lx.advance(4)
		} else {
			lx.advance(2)
		}
		return "·"
	case '\n', '\r':
		return ""
	}
	return string(c)
}

// template lexes `…${…}…`; the code in a hole is skipped.
func (lx *jslexer) template() {
	start := lx.line
	lx.advance(1)
	var b textBuilder
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == '`':
			lx.advance(1)
			lx.toks = append(lx.toks, jsTok{kind: tString, text: b.String(), line: start, endLine: lx.line, lines: b.lines})
			return
		case c == '\\':
			line := lx.line
			lx.advance(1)
			b.write(lx.escaped(), line)
		case strings.HasPrefix(lx.src[lx.pos:], "${"):
			line := lx.line
			lx.advance(2)
			lx.skipHole()
			b.write(hole, line)
		default:
			_, size := utf8.DecodeRuneInString(lx.src[lx.pos:])
			b.write(lx.src[lx.pos:lx.pos+size], lx.line)
			lx.advance(size)
		}
	}
	lx.toks = append(lx.toks, jsTok{kind: tString, text: b.String(), line: start, endLine: lx.line, lines: b.lines})
}

// skipHole skips the code of a ${…} hole, through nested braces, strings and
// templates, which are dropped: they belong to the hole.
func (lx *jslexer) skipHole() {
	depth := 0
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch c {
		case '"', '\'':
			n := len(lx.toks)
			lx.quoted(c)
			lx.toks = lx.toks[:n]
			continue
		case '`':
			n := len(lx.toks)
			lx.template()
			lx.toks = lx.toks[:n]
			continue
		case '{':
			depth++
		case '}':
			if depth == 0 {
				lx.advance(1)
				return
			}
			depth--
		}
		lx.advance(1)
	}
}

func (lx *jslexer) regex() {
	lx.advance(1)
	class := false
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == '\\':
			lx.advance(2)
			continue
		case c == '[':
			class = true
		case c == ']':
			class = false
		case c == '/' && !class:
			lx.advance(1)
			for lx.pos < len(lx.src) && isWordByte(lx.src[lx.pos]) {
				lx.pos++
			}
			lx.toks = append(lx.toks, jsTok{kind: tNumber, text: "/re/", line: lx.line, endLine: lx.line})
			return
		case c == '\n':
			return
		}
		lx.advance(1)
	}
}
