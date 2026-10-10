// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"fmt"
	"slices"
	"testing"
)

func cStrings(src string, d cDialect) []string {
	var out []string
	for _, t := range lexC(src, d) {
		if t.kind == tString {
			out = append(out, fmt.Sprintf("%d-%d:%s", t.line, t.endLine, t.text))
		}
	}
	return out
}

func TestLexSwift(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{`a("x \(f("y)")) z")`, []string{"1-1:x {} z"}},
		{`#"raw \(no) "quotes""#`, []string{`1-1:raw \(no) "quotes"`}},
		{`##"two \##(n) hashes"##`, []string{"1-1:two {} hashes"}},
		{"\"\"\"\n  one\n  two \\(n)\n  \"\"\" ; b", []string{"1-4:\n  one\n  two {}\n  "}},
		{"/* a /* \"nested\" */ \"still comment\" */ \"after\"", []string{"1-1:after"}},
		{"// \"line comment\"\n\"next\"", []string{"2-2:next"}},
		{`"esc \"q\" \n \u{1F600}"`, []string{"1-1:esc \"q\" \n ·"}},
		{"\"unterminated\nx\"", []string{"1-1:unterminated", "2-2:"}},
	}
	for _, c := range cases {
		if got := cStrings(c.src, dSwift); !slices.Equal(got, c.want) {
			t.Errorf("lexC(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestLexKotlin(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{`"a $name b ${f("c") + "}"} d"`, []string{"1-1:a {} b {} d"}},
		{"'\"' + \"x\"", []string{"1-1:x"}},
		{"\"\"\"\nraw \\n $x\n\"\"\"", []string{"1-3:\nraw \\n {}\n"}},
		{`"""quoted""""`, []string{`1-1:quoted"`}},
		{"`fun name`(\"in a call\")", []string{"1-1:in a call"}},
	}
	for _, c := range cases {
		if got := cStrings(c.src, dKotlin); !slices.Equal(got, c.want) {
			t.Errorf("lexC(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestLexJS(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{"const re = /\"x\"/g; const s = 'y';", []string{"1:y"}},
		{"a = b / c; d = 'e' / 2;", []string{"1:e"}},
		{"x = `a ${f(`inner ${'deep'}`)} b`;", []string{"1:a {} b"}},
		{"// 'comment'\n/* \"block\" */ s = \"z\"", []string{"2:z"}},
		// after ")" a slash is a division: "if (x) /re/" is the one case this
		// lexer gets wrong, and the pages don't write it
		{"x = (a) / 2; s = 'after'", []string{"1:after"}},
		{"`line one\nline two`", []string{"1:line one\nline two"}},
	}
	for _, c := range cases {
		var got []string
		for _, t := range lexJS(c.src, 1) {
			if t.kind == tString {
				got = append(got, fmt.Sprintf("%d:%s", t.line, t.text))
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("lexJS(%q) = %q, want %q", c.src, got, c.want)
		}
	}
	// each byte of a literal knows its line
	toks := lexJS("x = `a\nb ${c}\nd`", 5)
	lit := toks[len(toks)-1]
	if lit.text != "a\nb {}\nd" || lit.lines[0] != 5 || lit.lines[2] != 6 || lit.lines[len(lit.lines)-1] != 7 {
		t.Errorf("template %q lines %v", lit.text, lit.lines)
	}
}

func TestLexPython(t *testing.T) {
	src := "x = f\"a {b['c']} d\"  # 'comment'\ny = (\"one \"\n     \"two\")\nz = r'\\d+'\n\"\"\"doc\nstring\"\"\"\nw = '{{literal}}'\n"
	var got []string
	for _, t := range lexPython(src, 1) {
		if t.kind == tString {
			got = append(got, fmt.Sprintf("%d-%d:%s", t.line, t.endLine, t.text))
		}
	}
	want := []string{"1-1:a {} d", "2-3:one two", `4-4:\d+`, "5-6:doc\nstring", "7-7:{{literal}}"}
	if !slices.Equal(got, want) {
		t.Errorf("lexPython = %q, want %q", got, want)
	}
}
