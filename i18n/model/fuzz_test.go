// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzSourceFile feeds arbitrary bytes to the strings-file reader: it must
// never panic, and whatever it reads whole must come back identical from
// its own canonical form.
func FuzzSourceFile(f *testing.F) {
	for _, seed := range []string{
		`{"common.a": {"text": "A", "note": "n"}}`,
		`{"common.a": {"text": {"one": "a {n}", "other": "{n} b"}, "args": [["n", "count"]], "note": "n", "max": 9, "rich": ["b"], "stored": true, "review": "required", "reviewed": "x 2026-01-02", "translate": false}}`,
		`{"common.a": {"text": "Á", "en": "A", "reviewed": "ana 2026-10-13 #0123abcd"}}`,
		"{\"common.a\": {\"text\": \"\\u202e<script>\\t\", \"note\": \"\\u0000\"}}",
		`{"a": {"a": {"a": [[[[[]]]]]}}}`,
		strings.Repeat("[", 100),
		`{"x": 1, "x": 2}`,
	} {
		f.Add([]byte(seed), true)
		f.Add([]byte(seed), false)
	}
	f.Fuzz(func(t *testing.T, data []byte, english bool) {
		lang := "es"
		if english {
			lang = SourceCode
		}
		sf, _ := parseSourceFile(data, "i18n/strings/x/common.json", lang, "common")
		if sf.Broken {
			return
		}
		canon := sf.Canonical()
		again, ds := parseSourceFile(canon, sf.Path, lang, "common")
		if again.Broken {
			t.Fatalf("canonical form doesn't read back: %v\n%s", ds, canon)
		}
		if c2 := again.Canonical(); !bytes.Equal(c2, canon) {
			t.Fatalf("canonical form isn't stable:\n%s\n---\n%s", canon, c2)
		}
	})
}

// FuzzTokenize checks that Tokenize never panics and that what it accepts
// renders back to the same text.
func FuzzTokenize(f *testing.F) {
	for _, seed := range []string{"Hi {name}", "<b>x</b> {n}", "{{.}}", "</script><img onerror=x>", "a < b", "<a href=x>", "%s%n $&", "\"\"\""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		toks, err := Tokenize(s)
		if err != nil {
			return
		}
		var b strings.Builder
		for _, tk := range toks {
			switch tk.Kind {
			case Literal:
				if strings.ContainsAny(tk.Value, "<{}") {
					t.Fatalf("literal %q holds markup", tk.Value)
				}
				b.WriteString(tk.Value)
			case Placeholder:
				b.WriteString("{" + tk.Value + "}")
			case OpenTag:
				b.WriteString("<" + tk.Value + ">")
			case CloseTag:
				b.WriteString("</" + tk.Value + ">")
			}
		}
		if b.String() != s {
			t.Fatalf("tokens of %q render back as %q", s, b.String())
		}
	})
}
