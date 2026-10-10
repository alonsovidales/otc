// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// xcObject is a JSON object of a string catalog. Values are string, int, bool
// or xcObject.
type xcObject map[string]any

// xcFile writes a whole string catalog holding strs.
func xcFile(strs xcObject) []byte {
	var b strings.Builder
	writeXC(&b, xcObject{"sourceLanguage": "en", "strings": strs, "version": "1.0"}, "")
	return []byte(b.String())
}

// writeXC writes v the way Xcode writes a string catalog, so that a
// catalog Xcode opens and saves again comes out byte-identical: one member
// per line, `"key" : value`, two-space indent, members in Xcode's order
// (xcodeLess), an empty object as "{", a blank line and the closing brace,
// non-ASCII characters and "/" as they are, and no final newline.
func writeXC(b *strings.Builder, v any, indent string) {
	switch v := v.(type) {
	case string:
		writeXCString(b, v)
	case int:
		b.WriteString(strconv.Itoa(v))
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case xcObject:
		inner := indent + "  "
		if len(v) == 0 {
			b.WriteString("{\n\n" + indent + "}")
			return
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return xcodeLess(keys[i], keys[j]) })
		b.WriteString("{\n")
		for i, k := range keys {
			b.WriteString(inner)
			writeXCString(b, k)
			b.WriteString(" : ")
			writeXC(b, v[k], inner)
			if i < len(keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent + "}")
	default:
		panic(fmt.Sprintf("writeXC: unexpected %T", v))
	}
}

// writeXCString writes a JSON string as Xcode does: quotes, backslashes and
// control characters escaped (\n, \t, \r, else \u00xx), everything else as
// it is.
func writeXCString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// xcodeLess orders an object's members the way Xcode sorts them when it
// rewrites a catalog, which is the Finder's order (observed with Xcode 27's
// -exportLocalizations): letters without regard to case, runs of digits by
// their value ("a9" before "a10"), punctuation before digits before
// letters, "_" before "." ("ab_c" before "ab.d"). Ties - "a2" and "a02",
// "ab" and "Ab" - go to fewer leading zeros, then lower case.
func xcodeLess(a, b string) bool {
	primary, tie := xcodeCompare(a, b)
	if primary != 0 {
		return primary < 0
	}
	if tie != 0 {
		return tie < 0
	}
	return a < b
}

// xcodePunctuation is the order of ASCII punctuation in the Unicode
// collation Xcode's sort follows.
const xcodePunctuation = "_-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$"

// xcodeWeight returns a character's class (punctuation 0, digits 1,
// letters 2, anything else 3) and its weight within the class.
func xcodeWeight(r rune) (int, int) {
	switch {
	case r < utf8.RuneSelf && strings.ContainsRune(xcodePunctuation, r):
		return 0, strings.IndexRune(xcodePunctuation, r)
	case r >= '0' && r <= '9':
		return 1, int(r - '0')
	case r < utf8.RuneSelf && unicode.IsLetter(r):
		return 2, int(unicode.ToLower(r))
	}
	return 3, int(unicode.ToLower(r))
}

// xcodeCompare returns the primary comparison of a and b and, when that is
// equal, the first tie-breaking difference.
func xcodeCompare(a, b string) (primary, tie int) {
	cmp := func(x, y int) int {
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	digits := func(s string, i int) int {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		return j
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if xcIsDigit(a[i]) && xcIsDigit(b[j]) {
			ei, ej := digits(a, i), digits(b, j)
			da, db := strings.TrimLeft(a[i:ei], "0"), strings.TrimLeft(b[j:ej], "0")
			if c := cmp(len(da), len(db)); c != 0 {
				return c, 0
			}
			if c := strings.Compare(da, db); c != 0 {
				return c, 0
			}
			if tie == 0 {
				tie = cmp(ei-i, ej-j) // fewer leading zeros first
			}
			i, j = ei, ej
			continue
		}
		ra, na := utf8.DecodeRuneInString(a[i:])
		rb, nb := utf8.DecodeRuneInString(b[j:])
		ca, wa := xcodeWeight(ra)
		cb, wb := xcodeWeight(rb)
		if c := cmp(ca, cb); c != 0 {
			return c, 0
		}
		if c := cmp(wa, wb); c != 0 {
			return c, 0
		}
		if tie == 0 && ra != rb {
			tie = -cmp(int(ra), int(rb)) // lower case ('a' > 'A') first
		}
		i, j = i+na, j+nb
	}
	return cmp(len(a)-i, len(b)-j), tie
}

func xcIsDigit(c byte) bool { return c >= '0' && c <= '9' }
