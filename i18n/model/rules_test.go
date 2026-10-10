// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"fmt"
	"strings"
	"testing"
)

// entries builds an English file from key -> entry JSON pairs.
func entries(pairs ...string) string {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n%q: %s", pairs[i], pairs[i+1])
	}
	b.WriteString("\n}")
	return b.String()
}

func TestKeyPattern(t *testing.T) {
	for _, k := range []string{"common.ok", "app.photos.deleted_by", "dev.err.a1_b2_c3", "web.x9.y", "ios.a.1b"} {
		if !KeyPattern.MatchString(k) {
			t.Errorf("%q refused", k)
		}
	}
	for _, k := range []string{"common", "Common.ok", "common.OK", "common..ok", "common.ok.", "common._ok", "common.ok_", "common.a__b", "1common.ok", "common.ok-x", "common.ok x", "common_x.ok", ""} {
		if KeyPattern.MatchString(k) {
			t.Errorf("%q accepted", k)
		}
	}
	ds := check(t, map[string]string{
		enFile("common"): entries(
			"common.ok", `{"text": "OK", "note": "n"}`,
			"common.Bad", `{"text": "B", "note": "n"}`,
			"common."+strings.Repeat("x", MaxKeyLen), `{"text": "L", "note": "n"}`,
			"common.no_note", `{"text": "N"}`,
		),
	}, CheckOptions{})
	want(t, ds, Error, "common.Bad", "key must match")
	want(t, ds, Error, "common."+strings.Repeat("x", MaxKeyLen), "longer than 96")
	want(t, ds, Error, "common.no_note", `"note" is required`)
	wantNone(t, ds, Error, "common.ok", "")
}

func TestCollisionsAfterRenaming(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("ios"): entries(
			"ios.a_b", `{"text": "1", "note": "n"}`,
			"ios.a.b", `{"text": "2", "note": "n"}`,
			"ios.x1y", `{"text": "3", "note": "n"}`,
			"ios.x.1y", `{"text": "4", "note": "n"}`,
			"ios.fine", `{"text": "5", "note": "n"}`,
		),
		enFile("app.a_1"): `{}`,
		enFile("app.a1"):  `{}`,
	}, CheckOptions{})
	got := find(ds, Error, "", "collides with ios.a.b")
	if len(got) != 1 || !strings.Contains(got[0].Msg, "Android ios_a_b") || !strings.Contains(got[0].Msg, "Swift/Kotlin iosAB") || !strings.Contains(got[0].Msg, "Go IosAB") {
		t.Errorf("ios.a_b / ios.a.b: %s", dump(got))
	}
	got = find(ds, Error, "", "collides with ios.x.1y")
	if len(got) != 1 || strings.Contains(got[0].Msg, "Android") || !strings.Contains(got[0].Msg, "Swift/Kotlin iosX1y") {
		t.Errorf("ios.x1y / ios.x.1y should collide in Swift/Kotlin and Go only: %s", dump(got))
	}
	want(t, ds, Error, "", "prefix collides with app.a1 after renaming (Swift/Kotlin appA1, Go AppA1)")
	wantNone(t, ds, Error, "ios.fine", "")
}

func TestArguments(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"): entries(
			"common.ok", `{"text": "{name} and {n}", "args": [["name", "user"], ["n", "int"]], "note": "n"}`,
			"common.undeclared", `{"text": "Hi {name}", "note": "n"}`,
			"common.unused", `{"text": "Hi", "args": [["name", "user"]], "note": "n"}`,
			"common.bad_type", `{"text": "{x}", "args": [["x", "string"]], "note": "n"}`,
			"common.twice", `{"text": "{x}", "args": [["x", "text"], ["x", "text"]], "note": "n"}`,
			"common.bad_name", `{"text": "{x}", "args": [["X", "text"]], "note": "n"}`,
			"common.reserved", `{"text": "{default}", "args": [["default", "text"]], "note": "n"}`,
			"common.go_type", `{"text": "{type}", "args": [["type", "text"]], "note": "n"}`,
			"common.camel", `{"text": "{a_1} {a1}", "args": [["a_1", "text"], ["a1", "text"]], "note": "n"}`,
			"common.two_counts", `{"text": {"one": "{a} {b}", "other": "{a} {b}"}, "args": [["a", "count"], ["b", "count"]], "note": "n"}`,
			"common.plural_no_count", `{"text": {"one": "a", "other": "b"}, "note": "n"}`,
			"common.count_no_plural", `{"text": "{n} photos", "args": [["n", "count"]], "note": "n"}`,
			"common.bytes", `{"text": "{size}", "args": [["size", "bytes"]], "note": "n"}`,
		),
		enFile("dev"): entries(
			"dev.stored_types", `{"text": "{size} at {when}: {what}", "args": [["size", "bytes"], ["when", "datetime"], ["what", "msg"]], "note": "n"}`,
		),
		enFile("push"): entries(
			"push.ok", `{"text": "{name} shared {version} on {port}", "args": [["name", "user"], ["version", "text"], ["port", "text"]], "note": "n"}`,
			"push.leak", `{"text": "{name} uploaded {file}", "args": [["name", "user"], ["file", "text"]], "note": "n"}`,
		),
	}, CheckOptions{})
	want(t, ds, Error, "common.undeclared", "{name} is not a declared argument")
	want(t, ds, Error, "common.unused", "argument {name} is declared but not used")
	want(t, ds, Error, "common.bad_type", `unknown type "string"`)
	want(t, ds, Error, "common.twice", `argument "x" is declared twice`)
	want(t, ds, Error, "common.bad_name", `argument "X": names are lowercase`)
	want(t, ds, Error, "common.reserved", "reserved word")
	want(t, ds, Error, "common.go_type", "reserved word")
	want(t, ds, Error, "common.camel", `get the same name in generated code (a1)`)
	want(t, ds, Error, "common.two_counts", "at most one argument may be a count")
	want(t, ds, Error, "common.plural_no_count", "needs a count argument")
	want(t, ds, Error, "common.count_no_plural", "a count argument picks a plural form")
	want(t, ds, Error, "common.bytes", `type "bytes" only exists for keys only Go renders`)
	want(t, ds, Error, "push.leak", `argument "file": a push may only carry`)
	wantNone(t, ds, Error, "common.ok", "")
	wantNone(t, ds, Error, "dev.stored_types", "")
	wantNone(t, ds, Error, "push.ok", "")
}

func TestPluralForms(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"): entries(
			"common.photos", `{"text": {"one": "a photo", "other": "{n} photos"}, "args": [["n", "count"]], "note": "n"}`,
			"common.zero", `{"text": {"zero": "none", "one": "a photo", "other": "{n} photos"}, "args": [["n", "count"]], "note": "n"}`,
			"common.many", `{"text": {"one": "a photo", "other": "{n} photos", "many": "lots"}, "args": [["n", "count"]], "note": "n"}`,
			"common.no_one", `{"text": {"other": "{n} photos"}, "args": [["n", "count"]], "note": "n"}`,
			"common.other_no_count", `{"text": {"one": "{n} photo", "other": "photos"}, "args": [["n", "count"]], "note": "n"}`,
		),
		trFile("de", "common"): `{"common.photos": {"text": {"one": "ein Foto", "other": "{n} Fotos"}, "en": {"one": "a photo", "other": "{n} photos"}}}`,
		trFile("es", "common"): `{"common.photos": {"text": {"one": "una foto", "other": "{n} fotos"}, "en": {"one": "a photo", "other": "{n} photos"}}}`,
		trFile("fr", "common"): `{"common.photos": {"text": {"one": "une photo", "other": "{n} photos"}, "en": {"one": "a photo", "other": "{n} photos"}}}`,
	}, CheckOptions{})
	want(t, ds, Error, "common.zero", `plural form "zero": plurals are exactly "one" and "other"`)
	want(t, ds, Error, "common.many", `plural form "many"`)
	want(t, ds, Error, "common.no_one", `needs both "one" and "other"`)
	want(t, ds, Error, "common.other_no_count", "other: argument {n} is declared but not used")
	// Only French, where 0 is "one" too, must show the count in "one".
	if got := find(ds, Error, "common.photos", ""); len(got) != 1 || got[0].Lang != "fr" || !strings.Contains(got[0].Msg, `one: fr uses "one" for numbers other than 1 too`) {
		t.Errorf("want exactly the French one form refused: %s", dump(got))
	}
	frOK := check(t, map[string]string{
		enFile("common"):       entries("common.photos", `{"text": {"one": "a photo", "other": "{n} photos"}, "args": [["n", "count"]], "note": "n"}`),
		trFile("fr", "common"): `{"common.photos": {"text": {"one": "{n} photo", "other": "{n} photos"}, "en": {"one": "a photo", "other": "{n} photos"}}}`,
	}, CheckOptions{})
	wantNone(t, frOK, Error, "common.photos", "")
}

func TestOneMeansOne(t *testing.T) {
	for tag, wantOne := range map[string]bool{"en": true, "de": true, "nl": true, "es": true, "it": true, "pt-PT": true, "en-XA": true, "fr": false, "pt": false} {
		if got := (Language{Tag: tag}).OneMeansOne(); got != wantOne {
			t.Errorf("%s: OneMeansOne() = %v, want %v", tag, got, wantOne)
		}
	}
}

func TestTags(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"): entries(
			"common.rich", `{"text": "Read the <link>privacy policy</link>", "rich": ["link"], "note": "n"}`,
			"common.two", `{"text": "<b>{a}</b> and <i>{b}</i>", "args": [["a", "user"], ["b", "user"]], "rich": ["b", "i"], "note": "n"}`,
			"common.unlisted", `{"text": "a <b>bold</b> move", "note": "n"}`,
			"common.unused", `{"text": "plain", "rich": ["b"], "note": "n"}`,
			"common.nested", `{"text": "<b><i>x</i></b>", "rich": ["b", "i"], "note": "n"}`,
			"common.unbalanced", `{"text": "<b>x", "rich": ["b"], "note": "n"}`,
			"common.mismatched", `{"text": "<b>x</i>", "rich": ["b", "i"], "note": "n"}`,
			"common.attr", `{"text": "<a href=\"x\">x</a>", "rich": ["a"], "note": "n"}`,
			"common.bad_rich", `{"text": "<b>x</b>", "rich": ["b", "B"], "note": "n"}`,
		),
		enFile("dev"):  entries("dev.rich", `{"text": "<b>x</b>", "rich": ["b"], "note": "n"}`),
		enFile("mail"): entries("mail.rich", `{"text": "<b>x</b>", "rich": ["b"], "note": "n"}`),
		enFile("site"): entries("site.rich", `{"text": "Read <link>this</link>", "rich": ["link"], "note": "n"}`),
		trFile("es", "common"): `{
  "common.rich": {"text": "Lee la <a>política</a>", "en": "Read the <link>privacy policy</link>"},
  "common.two": {"text": "<i>{b}</i> y <b>{a}</b>", "en": "<b>{a}</b> and <i>{b}</i>"}
}`,
	}, CheckOptions{})
	wantNone(t, ds, Error, "common.two", "")
	want(t, ds, Error, "common.rich", "tags [a] differ from the English [link]")
	want(t, ds, Error, "common.unlisted", `tag <b> is not listed in "rich"`)
	want(t, ds, Error, "common.unused", `rich tag "b" is listed but the text never uses it`)
	want(t, ds, Error, "common.nested", "<i> inside <b> (tags don't nest)")
	want(t, ds, Error, "common.unbalanced", "<b> is never closed")
	want(t, ds, Error, "common.mismatched", "</i> where </b> was expected")
	want(t, ds, Error, "common.attr", `invalid tag "<a href=\"x\">"`)
	want(t, ds, Error, "common.bad_rich", `rich tag "B"`)
	want(t, ds, Error, "dev.rich", "rendered by Go as plain text and can't have tags")
	want(t, ds, Error, "mail.rich", "can't have tags")
	wantNone(t, ds, Error, "site.rich", "")
}

func TestTokenize(t *testing.T) {
	toks, err := Tokenize("Hi {name}, read <link>the {doc}</link> > now")
	if err != nil {
		t.Fatal(err)
	}
	wantToks := []Token{{Literal, "Hi "}, {Placeholder, "name"}, {Literal, ", read "}, {OpenTag, "link"}, {Literal, "the "}, {Placeholder, "doc"}, {CloseTag, "link"}, {Literal, " > now"}}
	if fmt.Sprint(toks) != fmt.Sprint(wantToks) {
		t.Errorf("got %v\nwant %v", toks, wantToks)
	}
	for _, bad := range []string{"{", "}", "{Name}", "{a b}", "{{.}}", "{}", "a < b", "<b", "<b>x", "</b>", "<b x>y</b>", "<!-- x -->", "<b>&amp;</b", "<1>x</1>", "<b><i>x</i></b>"} {
		if _, err := Tokenize(bad); err == nil {
			t.Errorf("Tokenize(%q) accepted", bad)
		}
	}
	if got := Placeholders("{a} {b} {a}"); fmt.Sprint(got) != "[a b]" {
		t.Errorf("Placeholders = %v", got)
	}
	if got := TextLength("<b>{name}</b> ok"); got != 9 {
		t.Errorf("TextLength = %d, want 9 ({name} counts 6, tags 0)", got)
	}
}

func TestHostileContent(t *testing.T) {
	type tc struct{ name, text string }
	cases := []tc{
		{"script", "</script><img onerror=alert(1)>"},
		{"onclick", "click onclick=steal()"},
		{"js url", "go to javascript:alert(1)"},
		{"spaced js", "go to java script:alert(1)"},
		{"data url", "data:text/html,boom"},
		{"entity", "Tom &amp; Jerry"},
		{"num entity", "&#60;b&#62;"},
		{"template", "{{.}}"},
		{"control", "bell\u0007"},
		{"bidi", "abc\u202edef"},
		{"isolate", "abc\u2066def"},
		{"private use", "a\ue000b"},
		{"bom", "a\ufeffb"},
		{"leading", " text"},
		{"trailing", "text "},
		{"double space", "two  spaces"},
		{"tab", "a\tb"},
		{"line space", "a \nb"},
		{"empty", ""},
		{"nbsp lead", "\u00a0text"},
	}
	var pairs []string
	for i, c := range cases {
		pairs = append(pairs, fmt.Sprintf("common.k%d", i), fmt.Sprintf(`{"text": %s, "note": "n"}`, js(c.text)))
	}
	ds := check(t, map[string]string{enFile("common"): entries(pairs...)}, CheckOptions{})
	for i, c := range cases {
		if len(find(ds, Error, fmt.Sprintf("common.k%d", i), "")) == 0 {
			t.Errorf("%s (%q) was accepted in English", c.name, c.text)
		}
	}

	// The same through a translation, plus links that aren't in the English.
	const english = "Read https://off-the.cloud/privacy or write to info@off-the.cloud"
	tr := []tc{
		{"script", "</script><img onerror=alert(1)>"},
		{"js url", "javascript:alert(1)"},
		{"url", "Ver https://evil.example/x"},
		{"email", "Escribe a a@evil.example"},
		{"domain", "Visita evil.example ahora"},
		{"bidi", "a\u202eb"},
		{"tab", "a\tb"},
		{"trailing", "Hola "},
		{"template", "{{.}}"},
		{"html attr", "<b onmouseover=x>hola</b>"},
		{"stray tags", "<b>hola</b>"},
	}
	var en, es []string
	for i, c := range tr {
		k := fmt.Sprintf("common.t%d", i)
		en = append(en, k, fmt.Sprintf(`{"text": %s, "note": "n"}`, js(english)))
		es = append(es, k, fmt.Sprintf(`{"text": %s, "en": %s}`, js(c.text), js(english)))
	}
	en = append(en, "common.links", fmt.Sprintf(`{"text": %s, "note": "n"}`, js(english)))
	es = append(es, "common.links", fmt.Sprintf(`{"text": %s, "en": %s}`, js("Lee https://off-the.cloud/privacy o escribe a info@off-the.cloud"), js(english)))
	ds = check(t, map[string]string{enFile("common"): entries(en...), trFile("es", "common"): entries(es...)}, CheckOptions{})
	for i, c := range tr {
		k := fmt.Sprintf("common.t%d", i)
		if got := find(ds, Error, k, ""); len(got) == 0 || got[0].Lang != "es" {
			t.Errorf("%s (%q) was accepted in a translation", c.name, c.text)
		}
	}
	wantNone(t, ds, Error, "common.links", "")
	want(t, ds, Error, "", `"https://evil.example/x" is not in the English`)
	want(t, ds, Error, "", `"a@evil.example" is not in the English`)
	want(t, ds, Error, "", `"evil.example" is not in the English`)
}

func TestLiteralCharactersStayAllowed(t *testing.T) {
	// What the emitters must escape is still allowed in the catalog: the
	// hostile-catalog test runs these through every emitter.
	ds := check(t, map[string]string{
		enFile("common"): entries(
			"common.quotes", `{"text": "\"\"\" it's", "note": "n"}`,
			"common.printf", `{"text": "100% done %s%n $& $1", "note": "n"}`,
			"common.amp", `{"text": "Save & Retry > later", "note": "n"}`,
			"common.lines", `{"text": "one\ntwo", "note": "n"}`,
			"common.colon", `{"text": "This needs JavaScript: turn it on. Your data: safe.", "note": "n"}`,
			"common.french", `{"text": "Fichiers : « ici »", "note": "n"}`,
			"common.at", `{"text": "@home ?really", "note": "n"}`,
		),
	}, CheckOptions{})
	wantNoErrors(t, ds)
}

func TestMaxLength(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"): entries(
			"common.short", `{"text": "<b>{name}</b> left", "args": [["name", "user"]], "rich": ["b"], "max": 11, "note": "n"}`,
			"common.long", `{"text": "Far too long", "max": 5, "note": "n"}`,
			"common.plural", `{"text": {"one": "a photo", "other": "{n} photos"}, "args": [["n", "count"]], "max": 10, "note": "n"}`,
		),
		trFile("es", "common"): entries(
			"common.short", `{"text": "<b>{name}</b> se fue ya", "en": "<b>{name}</b> left"}`,
			"common.plural", `{"text": {"one": "una foto", "other": "{n} fotografías"}, "en": {"one": "a photo", "other": "{n} photos"}}`,
		),
	}, CheckOptions{})
	if got := find(ds, Error, "common.short", "more than"); len(got) != 1 || got[0].Lang != "es" {
		t.Errorf("only the Spanish common.short is too long: %s", dump(got))
	}
	want(t, ds, Error, "common.long", `is 12 characters, more than "max" 5`)
	want(t, ds, Error, "common.plural", `other: is 15 characters, more than "max" 10`)
}
