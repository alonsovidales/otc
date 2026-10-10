// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"strings"
	"unicode/utf8"
)

var pseudoLetters = map[rune]rune{
	'a': 'á', 'c': 'ç', 'e': 'é', 'i': 'í', 'n': 'ñ', 'o': 'ó', 's': 'š', 'u': 'ú', 'y': 'ý', 'z': 'ž',
	'A': 'Á', 'C': 'Ç', 'E': 'É', 'I': 'Í', 'N': 'Ñ', 'O': 'Ó', 'S': 'Š', 'U': 'Ú', 'Y': 'Ý', 'Z': 'Ž',
}

// PseudoText is a text in the pseudo-locale: letters accented, each form in
// brackets and padded with "~" by about a third of its length, as the
// longer languages run. Placeholders and tags are kept exactly, so the
// text still renders through the real accessors. "Delete {count} photos"
// becomes "[Délété {count} phótóš ~~~~~~]".
func PseudoText(t Text) Text { return t.Map(pseudoForm) }

func pseudoForm(s string) string {
	toks, err := Tokenize(s)
	if err != nil {
		return s
	}
	var b strings.Builder
	b.WriteString("[")
	letters := 0
	for _, t := range toks {
		switch t.Kind {
		case Literal:
			for _, r := range t.Value {
				if p, ok := pseudoLetters[r]; ok {
					r = p
				}
				b.WriteRune(r)
			}
			letters += utf8.RuneCountInString(t.Value)
		case Placeholder:
			b.WriteString("{" + t.Value + "}")
		case OpenTag:
			b.WriteString("<" + t.Value + ">")
		case CloseTag:
			b.WriteString("</" + t.Value + ">")
		}
	}
	b.WriteString(" ")
	b.WriteString(strings.Repeat("~", max(1, (letters+2)/3)))
	b.WriteString("]")
	return b.String()
}
