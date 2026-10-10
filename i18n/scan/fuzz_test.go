// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"testing"
)

// FuzzLexers: no input makes a lexer or the HTML scan panic or hang, and
// every literal's lines are within the input.
func FuzzLexers(f *testing.F) {
	for _, seed := range []string{
		`Text("a \(b("c")) d")`, `#"raw"#`, "\"\"\"\nx\n\"\"\"", `"$a ${b + "}"}"`, "'\\''",
		"x = `a ${`b ${c}`} d` / 2", "/re[/]/g", `f"{a['b']}" "c"`, "<p>a <b>b</b></p><script>x='<i>y</i>'</script>",
		"/* /* */", `"\u{`, "`${", "<script type=\"application/json\">{\"a\": \"b\"}</script>",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		lines := 1
		for _, c := range src {
			if c == '\n' {
				lines++
			}
		}
		for _, d := range []cDialect{dSwift, dKotlin} {
			for _, tk := range lexC(src, d) {
				if tk.line < 1 || tk.endLine < tk.line || tk.endLine > lines {
					t.Fatalf("lexC: token %+v outside %d lines", tk, lines)
				}
			}
		}
		for _, tk := range lexJS(src, 1) {
			if tk.line < 1 || tk.endLine < tk.line || tk.endLine > lines || len(tk.lines) != len(tk.text) && tk.kind == tString {
				t.Fatalf("lexJS: token %+v outside %d lines", tk, lines)
			}
		}
		for _, tk := range lexPython(src, 1) {
			if tk.line < 1 || tk.endLine < tk.line || tk.endLine > lines {
				t.Fatalf("lexPython: token %+v outside %d lines", tk, lines)
			}
		}
		s := &scanner{surface: "pages", never: newNeverTerms([]string{"Off The Cloud"}), lines: map[string][]string{}}
		s.scanHTML("x.html", []byte(src), newLineIndex([]byte(src)).line)
		for _, fd := range s.found {
			if fd.Line < 1 || fd.Line > lines {
				t.Fatalf("html: finding %+v outside %d lines", fd, lines)
			}
		}
	})
}
