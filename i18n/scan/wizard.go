// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"strings"
	"unicode/utf8"
)

// The setup wizard (scripts/setup_wizard.py): its page, the raw string
// PAGE = r"""…""", is scanned like the bridge's pages; its backend's replies
// are the "error" values it sends ({"error": "…"},
// data.get("error", "…"), data.get("error") or "…").

const wizardFile = "scripts/setup_wizard.py"

func scanWizard(s *scanner) error {
	if !s.exists(wizardFile) {
		return nil
	}
	src, err := s.source(wizardFile)
	if err != nil {
		return err
	}
	lines := newLineIndex(src)
	text := string(src)
	pageStart, pageEnd := -1, -1
	for _, open := range []string{`PAGE = r"""`, `PAGE = """`, `PAGE = r'''`} {
		if i := strings.Index(text, open); i >= 0 {
			pageStart = i + len(open)
			if j := strings.Index(text[pageStart:], open[len(open)-3:]); j >= 0 {
				pageEnd = pageStart + j
			}
			break
		}
	}
	if pageStart >= 0 && pageEnd > pageStart {
		base := pageStart
		s.scanHTML(wizardFile, src[pageStart:pageEnd], func(off int) int { return lines.line(base + off) })
	} else {
		pageStart, pageEnd = len(text), len(text)
	}
	toks := lexPython(text[:pageStart], 1)
	toks = append(toks, lexPython(text[pageEnd:], lines.line(pageEnd))...)
	isErr := func(t ctok) bool { return t.kind == tString && t.text == "error" }
	for i, t := range toks {
		if t.kind != tString || isErr(t) {
			continue
		}
		at := func(j int) ctok {
			if j < 0 || j >= len(toks) {
				return ctok{kind: tPunct}
			}
			return toks[j]
		}
		p1, p2, p3 := at(i-1), at(i-2), at(i-3)
		rule := ""
		switch {
		case p1.text == ":" && isErr(p2): // {"error": "…"}
			rule = "key:error"
		case p1.text == "," && isErr(p2) && p3.text == "(" && at(i-4).text == "get": // .get("error", "…")
			rule = "fallback"
		case p1.kind == tIdent && p1.text == "or" && p2.text == ")" && isErr(p3): // .get("error") or "…"
			rule = "fallback"
		}
		if rule != "" && p1.kind != tString && s.looseText(t.text) {
			s.add(wizardFile, t.line, t.endLine, rule, t.text)
		}
	}
	return nil
}

// lexPython finds Python string literals (prefixes, triple quotes, f-string
// holes) and the identifiers and punctuation around them; adjacent literals
// are joined into one, as Python does.
func lexPython(src string, firstLine int) []ctok {
	var toks []ctok
	line := firstLine
	depth := 0        // open brackets: a newline inside them doesn't end the statement
	joinable := false // the last token is a literal a following one may join
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
			if depth == 0 {
				joinable = false
			}
		case c == ' ' || c == '\t' || c == '\r' || c == '\\':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'' || isStrPrefix(src[i:]):
			j := i
			for src[j] != '"' && src[j] != '\'' {
				j++
			}
			prefix := strings.ToLower(src[i:j])
			q := string(src[j])
			if strings.HasPrefix(src[j:], q+q+q) {
				q = q + q + q
			}
			start := line
			k := j + len(q)
			var b strings.Builder
			for k < len(src) && !strings.HasPrefix(src[k:], q) {
				ch := src[k]
				switch {
				case ch == '\\' && !strings.Contains(prefix, "r") && k+1 < len(src):
					if src[k+1] == '\n' {
						line++
					}
					b.WriteByte(src[k+1])
					k += 2
					continue
				case ch == '\n':
					line++
					if len(q) == 1 {
						k = len(src) // unterminated
						continue
					}
				case ch == '{' && strings.Contains(prefix, "f"):
					if k+1 < len(src) && src[k+1] == '{' {
						b.WriteByte('{')
						k += 2
						continue
					}
					depth := 0
					for k < len(src) {
						if src[k] == '{' {
							depth++
						} else if src[k] == '}' {
							depth--
							if depth == 0 {
								break
							}
						} else if src[k] == '\n' {
							line++
						}
						k++
					}
					b.WriteString(hole)
					k++
					continue
				}
				_, size := utf8.DecodeRuneInString(src[k:])
				b.WriteString(src[k : k+size])
				k += size
			}
			i = k + len(q)
			if n := len(toks); n > 0 && toks[n-1].kind == tString && joinable {
				toks[n-1].text += b.String() // "a" "b" is one literal
				toks[n-1].endLine = line
				continue
			}
			toks = append(toks, ctok{kind: tString, text: b.String(), line: start, endLine: line})
			joinable = true
		case isIdentStart(src[i:]):
			j := i
			for j < len(src) && (isWordByte(src[j]) || src[j] >= 0x80) {
				j++
			}
			toks = append(toks, ctok{kind: tIdent, text: src[i:j], line: line, endLine: line})
			joinable = false
			i = j
		default:
			_, size := utf8.DecodeRuneInString(src[i:])
			switch c {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				if depth > 0 {
					depth--
				}
			}
			toks = append(toks, ctok{kind: tPunct, text: src[i : i+size], line: line, endLine: line})
			joinable = false
			i += size
		}
	}
	return toks
}

// isStrPrefix reports whether s starts with a string prefix and a quote
// (f"", rb”, ...).
func isStrPrefix(s string) bool {
	for n := 1; n <= 2 && n < len(s); n++ {
		p := strings.ToLower(s[:n])
		if strings.Trim(p, "rbfu") == "" && (s[n] == '"' || s[n] == '\'') {
			return true
		}
	}
	return false
}
