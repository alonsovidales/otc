// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import (
	"testing"

	"github.com/alonsovidales/otc/i18n/model"
)

var tokenCorpus = []string{
	"", "plain", "{name}", "a {name} b", "{a}{b}", "{a_b1}", "{a__b}", "{_a}", "{a_}", "{A}", "{1a}", "{}",
	"{name", "name}", "{{name}}", "{na{me}", "x } y", "<b>x</b>", "<b>{n}</b> y", "<b></b>", "<b>x", "x</b>",
	"<b><i>x</i></b>", "<b>x</i>", "</b>", "<b x>y</b>", "<B>x</B>", "<1>x</1>", "a < b", "a > b", "<>", "</>",
	"<b>x</b><i>y</i>", "100% $& %s \\ \"", "é {n} ü", "<link>{name}</link>.", "{a}<b>{c}</b>{d}", "<a1>x</a1>",
	"\x00{n}", "{n}\xff", "<b>\n</b>", "{n}<", "<", "{", "}",
}

// The runtime's tokenizer reads the generated catalog with the grammar
// model.Tokenize checked it against: the same tokens, and malformed
// exactly when model says so.
func TestTokenizeMatchesModel(t *testing.T) {
	for _, s := range tokenCorpus {
		compareTokenize(t, s)
	}
}

func FuzzTokenizeMatchesModel(f *testing.F) {
	for _, s := range tokenCorpus {
		f.Add(s)
	}
	f.Fuzz(compareTokenize)
}

func compareTokenize(t *testing.T, s string) {
	want, werr := model.Tokenize(s)
	got, ok := tokenize(s)
	if ok != (werr == nil) {
		t.Fatalf("%q: tokenize ok=%v, model.Tokenize error %v", s, ok, werr)
	}
	if !ok {
		return
	}
	if len(got) != len(want) {
		t.Fatalf("%q: %v, model %v", s, got, want)
	}
	kinds := map[model.TokenKind]tokKind{model.Literal: tokLit, model.Placeholder: tokArg, model.OpenTag: tokOpen, model.CloseTag: tokClose}
	for i := range got {
		if got[i].kind != kinds[want[i].Kind] || got[i].val != want[i].Value {
			t.Fatalf("%q: token %d is %v, model %v", s, i, got[i], want[i])
		}
	}
}
