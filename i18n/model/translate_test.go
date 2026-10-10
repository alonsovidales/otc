// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"strings"
	"testing"
)

// The English shared by the translation tests.
var baseEnglish = entries(
	"common.save", `{"text": "Save", "note": "n"}`,
	"common.photos", `{"text": {"one": "a photo", "other": "{n} photos"}, "args": [["n", "count"]], "note": "n"}`,
)

func TestStaleAndMissingTranslations(t *testing.T) {
	files := map[string]string{
		enFile("common"): baseEnglish,
		// German (shipping) is stale on save and lacks photos; Spanish
		// (draft) the same.
		trFile("de", "common"): `{"common.save": {"text": "Sichern", "en": "Store"}}`,
		trFile("es", "common"): `{"common.save": {"text": "Guardar", "en": "Store"}}`,
		trFile("fr", "common"): `{"common.save": {"text": "Enregistrer", "en": "Save"}, "common.photos": {"text": {"one": "{n} photo", "other": "{n} photos"}, "en": {"one": "a photo", "other": "{n} photos"}}}`,
	}
	type sev struct {
		lang, key, msg string
		want           Severity
	}
	for _, tc := range []struct {
		name string
		opts CheckOptions
		want []sev
	}{
		{"development", CheckOptions{}, []sev{
			{"de", "common.save", "stale", Warning}, {"de", "common.photos", "not translated", Warning},
			{"es", "common.save", "stale", Info}, {"es", "common.photos", "not translated", Info},
		}},
		{"release", CheckOptions{Release: true}, []sev{
			{"de", "common.save", "stale", Error}, {"de", "common.photos", "not translated", Error},
			{"es", "common.save", "stale", Info}, {"es", "common.photos", "not translated", Info},
		}},
		{"release of a draft named with -lang", CheckOptions{Release: true, Langs: []string{"es"}}, []sev{
			{"es", "common.save", "stale", Error}, {"es", "common.photos", "not translated", Error},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := check(t, files, tc.opts)
			for _, w := range tc.want {
				found := false
				for _, d := range find(ds, w.want, w.key, w.msg) {
					found = found || d.Lang == w.lang
				}
				if !found {
					t.Errorf("no %s %s %q for %s:\n%s", w.want, w.key, w.msg, w.lang, dump(ds))
				}
			}
			for _, d := range ds {
				if d.Lang == "fr" && d.Severity > Info {
					t.Errorf("French is complete and current: %s", d)
				}
				if len(tc.opts.Langs) > 0 && d.Lang == "de" {
					t.Errorf("-lang es should hide German: %s", d)
				}
			}
		})
	}

	c := load(t, files)
	de, _ := c.Language("de")
	es, _ := c.Language("es")
	fr, _ := c.Language("fr")
	en, _ := c.Language("en")
	for _, tc := range []struct {
		lang  Language
		key   string
		state State
		text  string
	}{
		{en, "common.save", StateSource, "Save"},
		{de, "common.save", StateStale, "Save"},
		{de, "common.photos", StateMissing, "{n} photos"},
		{es, "common.save", StateStale, "Save"},
		{fr, "common.save", StateTranslated, "Enregistrer"},
		{fr, "common.photos", StateTranslated, "{n} photos"},
		{PseudoLanguage, "common.save", StatePseudo, "[Šávé ~~]"},
	} {
		r, ok := c.Resolve(tc.lang, tc.key)
		if !ok || r.State != tc.state || r.Text.Other != tc.text {
			t.Errorf("Resolve(%s, %s) = %v %q, want %v %q", tc.lang.Code, tc.key, r.State, r.Text.Other, tc.state, tc.text)
		}
	}
	if r, _ := c.Resolve(de, "common.save"); r.Translation == nil || r.Translation.Text.Other != "Sichern" || !r.Fallback() {
		t.Errorf("a stale translation stays visible to tools: %+v", r.Translation)
	}
	if cov := c.Coverage(es); cov[StateStale] != 1 || cov[StateMissing] != 1 {
		t.Errorf("Coverage(es) = %v", cov)
	}
}

func TestStaleTranslationSkipsComparisons(t *testing.T) {
	// The English gained an argument: the old translation is stale, and
	// its old placeholders aren't reported on top of that.
	ds := check(t, map[string]string{
		enFile("common"):       entries("common.hi", `{"text": "Hi {name}, welcome", "args": [["name", "user"]], "note": "n"}`),
		trFile("es", "common"): `{"common.hi": {"text": "Hola", "en": "Hi"}}`,
	}, CheckOptions{})
	want(t, ds, Info, "common.hi", "stale")
	wantNone(t, ds, Error, "common.hi", "")
	// ... but what a form may contain still applies.
	ds = check(t, map[string]string{
		enFile("common"):       entries("common.hi", `{"text": "Hi", "note": "n"}`),
		trFile("es", "common"): `{"common.hi": {"text": "Hola\u202e", "en": "Hello"}}`,
	}, CheckOptions{})
	want(t, ds, Error, "common.hi", "bidi control character")
}

func TestReviewRequired(t *testing.T) {
	// No review yet: work to do, not an error, until the release check.
	files := map[string]string{
		enFile("common"): entries("common.erase", `{"text": "The disk will be erased", "note": "n", "review": "required"}`),
	}
	ds := check(t, files, CheckOptions{})
	want(t, ds, Warning, "common.erase", "needs a review")
	ds = check(t, files, CheckOptions{Release: true})
	want(t, ds, Error, "common.erase", "needs a review")

	// A reviewer writes "<who> <date>"; the canonical rewrite records the
	// fingerprint, and the review then holds.
	files[enFile("common")] = entries("common.erase", `{"text": "The disk will be erased", "note": "n", "review": "required", "reviewed": "owner 2026-10-12"}`)
	c := load(t, files)
	want(t, c.Check(CheckOptions{}), Error, "common.erase", "has no fingerprint yet")
	if len(c.NonCanonical(CheckOptions{})) != 1 {
		t.Fatal("a review without its fingerprint is not canonical")
	}
	canon := string(c.Files()[0].Canonical())
	fp := Fingerprint(PlainText("The disk will be erased"))
	if !strings.Contains(canon, `"reviewed": "owner 2026-10-12 #`+fp+`"`) {
		t.Fatalf("canonical form doesn't record the fingerprint:\n%s", canon)
	}
	files[enFile("common")] = canon
	ds = check(t, files, CheckOptions{Release: true})
	wantNone(t, ds, Error, "common.erase", "review")
	wantNone(t, ds, Warning, "common.erase", "review")

	// Changing the English makes the review stale.
	files[enFile("common")] = strings.Replace(canon, "will be erased", "will be wiped", 1)
	ds = check(t, files, CheckOptions{})
	want(t, ds, Warning, "common.erase", "the review by owner on 2026-10-12 is stale")

	// Malformed values, and reviews where none is asked for.
	ds = check(t, map[string]string{
		enFile("common"): entries(
			"common.a", `{"text": "A", "note": "n", "review": "required", "reviewed": "2026-10-12"}`,
			"common.b", `{"text": "B", "note": "n", "review": "required", "reviewed": "owner 12/10/2026"}`,
			"common.c", `{"text": "C", "note": "n", "review": "maybe"}`,
			"common.d", `{"text": "D", "note": "n", "reviewed": "owner 2026-10-12"}`,
		),
		trFile("es", "common"): `{"common.d": {"text": "D", "en": "D", "reviewed": "owner 2026-10-12"}}`,
	}, CheckOptions{})
	want(t, ds, Error, "common.a", `is not "<who> <YYYY-MM-DD>"`)
	want(t, ds, Error, "common.b", "the date must be YYYY-MM-DD")
	want(t, ds, Error, "common.c", `"review" can only be "required"`)
	want(t, ds, Error, "common.d", `"reviewed" is only for keys with "review"`)
	if got := find(ds, Error, "common.d", `"reviewed" is only for keys`); len(got) != 2 {
		t.Errorf("both the English and the Spanish common.d should be refused: %s", dump(got))
	}
}

func TestReviewedTranslations(t *testing.T) {
	en := entries("common.erase", `{"text": "Erase", "note": "n", "review": "required", "reviewed": "owner 2026-10-12 #`+Fingerprint(PlainText("Erase"))+`"}`)
	reviewed := "ana 2026-10-13 #" + Fingerprint(PlainText("Löschen"), PlainText("Erase"))
	files := map[string]string{
		enFile("common"):       en,
		trFile("de", "common"): `{"common.erase": {"text": "Löschen", "en": "Erase"}}`,
	}
	ds := check(t, files, CheckOptions{})
	want(t, ds, Warning, "common.erase", "needs a review")
	c := load(t, files)
	de, _ := c.Language("de")
	if r, _ := c.Resolve(de, "common.erase"); r.State != StateUnreviewed || r.Text.Other != "Löschen" {
		t.Errorf("an unreviewed translation is used and marked: %v %q", r.State, r.Text.Other)
	}

	files[trFile("de", "common")] = `{"common.erase": {"text": "Löschen", "en": "Erase", "reviewed": "` + reviewed + `"}}`
	ds = check(t, files, CheckOptions{Release: true})
	wantNone(t, ds, Error, "common.erase", "")
	c = load(t, files)
	if r, _ := c.Resolve(de, "common.erase"); r.State != StateTranslated {
		t.Errorf("a reviewed translation is translated: %v", r.State)
	}

	// Editing the reviewed translation voids the review.
	files[trFile("de", "common")] = `{"common.erase": {"text": "Entfernen", "en": "Erase", "reviewed": "` + reviewed + `"}}`
	ds = check(t, files, CheckOptions{Release: true})
	want(t, ds, Error, "common.erase", "the review by ana on 2026-10-13 is stale")
}

func TestNeverTranslate(t *testing.T) {
	en := entries(
		"common.brand", `{"text": "Off The Cloud uses Tailscale", "note": "n"}`,
		"common.plain", `{"text": "Hello", "note": "n"}`,
	)
	ds := check(t, map[string]string{
		enFile("common"):       en,
		trFile("es", "common"): `{"common.brand": {"text": "Fuera de la Nube usa Tailscale", "en": "Off The Cloud uses Tailscale"}, "common.plain": {"text": "Hola Apple", "en": "Hello"}}`,
	}, CheckOptions{})
	want(t, ds, Error, "common.brand", `"Off The Cloud" is never translated`)
	wantNone(t, ds, Error, "common.brand", `"Tailscale"`)
	wantNone(t, ds, Error, "common.plain", "")

	// never.json replaces the defaults.
	ds = check(t, map[string]string{
		enFile("common"):       en,
		NeverFile:              `{"never": ["Tailscale"]}`,
		trFile("es", "common"): `{"common.brand": {"text": "Fuera de la Nube usa Escala", "en": "Off The Cloud uses Tailscale"}}`,
	}, CheckOptions{})
	want(t, ds, Error, "common.brand", `"Tailscale" is never translated`)
	wantNone(t, ds, Error, "common.brand", `"Off The Cloud"`)

	// Whole words in the English, case included; any word in the translation.
	ds = check(t, map[string]string{
		enFile("common"): entries(
			"common.mac", `{"text": "Open the Mac app on your iPhone", "note": "n"}`,
			"common.machine", `{"text": "Machine learning on the SDK", "note": "n"}`,
			"common.lower", `{"text": "an apple a day", "note": "n"}`,
		),
		NeverFile: `{"never": ["Mac", "iPhone", "SD", "Apple"]}`,
		trFile("de", "common"): entries(
			"common.mac", `{"text": "Öffne die Mac-App des iPhones", "en": "Open the Mac app on your iPhone"}`,
			"common.machine", `{"text": "Maschinelles Lernen im Kit", "en": "Machine learning on the SDK"}`,
			"common.lower", `{"text": "ein Apfel am Tag", "en": "an apple a day"}`,
		),
	}, CheckOptions{})
	wantNoErrors(t, ds)
	ds = check(t, map[string]string{
		enFile("common"):       entries("common.mac", `{"text": "Open the Mac app", "note": "n"}`),
		NeverFile:              `{"never": ["Mac"]}`,
		trFile("de", "common"): entries("common.mac", `{"text": "Öffne die Macintosh-App", "en": "Open the Mac app"}`),
	}, CheckOptions{})
	wantNoErrors(t, ds) // "Mac" is inside "Macintosh": accepted, as the German genitive needs
	ds = check(t, map[string]string{
		enFile("common"):       entries("common.mac", `{"text": "Open the Mac app", "note": "n"}`),
		NeverFile:              `{"never": ["Mac"]}`,
		trFile("de", "common"): entries("common.mac", `{"text": "Öffne die mac-App", "en": "Open the Mac app"}`),
	}, CheckOptions{})
	want(t, ds, Error, "common.mac", `"Mac" is never translated`)

	ds = check(t, map[string]string{enFile("common"): en, NeverFile: `{"never": "Tailscale"}`}, CheckOptions{})
	want(t, ds, Error, "", `"never" must be an array`)
}

func TestGlossary(t *testing.T) {
	files := map[string]string{
		enFile("common"): entries(
			"common.coll", `{"text": "Your Collections", "note": "n"}`,
			"common.word", `{"text": "Collectionsx and recollections", "note": "n"}`,
			"common.album", `{"text": "Photos", "note": "n"}`,
		),
		GlossaryDir + "/es.json": `{"code": "es",
  "terms": [{"en": "collections", "text": "Colecciones", "note": "our word", "enforce": true},
            {"en": "Photos", "text": "Fotos", "enforce": false}],
  "forbidden": [{"text": "álbum", "note": "we say colecciones"}]}`,
		trFile("es", "common"): `{
  "common.coll": {"text": "Tus álbumes", "en": "Your Collections"},
  "common.word": {"text": "Algo", "en": "Collectionsx and recollections"},
  "common.album": {"text": "Álbum de imágenes", "en": "Photos"}
}`,
	}
	ds := check(t, files, CheckOptions{})
	want(t, ds, Warning, "common.coll", `the glossary translates "collections" as "Colecciones"`)
	want(t, ds, Warning, "common.album", `the glossary says not to use "álbum" (we say colecciones)`)
	wantNone(t, ds, Warning, "common.coll", "álbum") // "álbumes" is another word
	wantNone(t, ds, Warning, "common.word", "glossary")
	wantNone(t, ds, Warning, "common.album", "Fotos") // not enforced
	ds = check(t, files, CheckOptions{Strict: true})
	want(t, ds, Error, "common.coll", "the glossary translates")
	want(t, ds, Error, "common.album", "the glossary says not to use")

	ds = check(t, map[string]string{
		enFile("common"):         baseEnglish,
		GlossaryDir + "/es.json": `{"code": "fr", "terms": [{"en": "x"}], "extra": 1}`,
		GlossaryDir + "/xx.json": `{"code": "xx"}`,
		GlossaryDir + "/en.json": `{"code": "en"}`,
	}, CheckOptions{})
	want(t, ds, Error, "", `"code" must be "es"`)
	want(t, ds, Error, "", `a term needs "en" and "text"`)
	want(t, ds, Error, "", `unknown field "extra"`)
	want(t, ds, Error, "", `glossary for "xx"`)
	want(t, ds, Error, "", `glossary for "en"`)
}

func TestContainsWordFold(t *testing.T) {
	for _, tc := range []struct {
		s, w string
		want bool
	}{
		{"Your Collections here", "collections", true},
		{"collections", "Collections", true},
		{"recollections", "collections", false},
		{"Collectionsx", "collections", false},
		{"(Collections)", "collections", true},
		{"ÁLBUM", "álbum", true},
		{"álbumes", "álbum", false},
		{"x", "", false},
	} {
		if got := containsWordFold(tc.s, tc.w); got != tc.want {
			t.Errorf("containsWordFold(%q, %q) = %v", tc.s, tc.w, got)
		}
	}
}

func TestTranslateFalse(t *testing.T) {
	files := map[string]string{
		enFile("common"): entries(
			"common.brand", `{"text": "Off The Cloud", "note": "n", "translate": false}`,
			"common.word", `{"text": "delete", "note": "n", "translate": false}`,
		),
		trFile("es", "common"): `{"common.brand": {"text": "Off The Cloud", "en": "Off The Cloud"}, "common.word": {"text": "borrar", "en": "delete"}}`,
	}
	ds := check(t, files, CheckOptions{Release: true, Langs: []string{"es"}})
	wantNone(t, ds, Error, "common.brand", "")
	want(t, ds, Error, "common.word", `"translate" is false`)
	wantNone(t, ds, Error, "common.word", "not translated")
	c := load(t, files)
	es, _ := c.Language("es")
	for _, k := range []string{"common.brand", "common.word"} {
		if r, _ := c.Resolve(es, k); r.State != StateSource || r.Text.Other != c.Entry(k).Text.Other {
			t.Errorf("%s resolves to %v %q, want the English", k, r.State, r.Text.Other)
		}
	}
	if r, _ := c.Resolve(PseudoLanguage, "common.word"); r.Text.Other != "delete" {
		t.Errorf("untranslatable text stays as it is in the pseudo-locale: %q", r.Text.Other)
	}
}

func TestPseudoText(t *testing.T) {
	got := PseudoText(PluralText("Delete a photo", "Delete {count} <b>photos</b>"))
	if got.One != "[Délété á phótó ~~~~~]" {
		t.Errorf("one = %q", got.One)
	}
	if !strings.HasPrefix(got.Other, "[Délété {count} <b>phótóš</b> ~") || !strings.HasSuffix(got.Other, "]") {
		t.Errorf("other = %q", got.Other)
	}
	if _, err := Tokenize(got.Other); err != nil {
		t.Errorf("pseudo text must still tokenize: %v", err)
	}
	if p := Placeholders(got.Other); len(p) != 1 || p[0] != "count" {
		t.Errorf("placeholders = %v", p)
	}
}

func TestOutputLanguages(t *testing.T) {
	c := load(t, map[string]string{enFile("common"): baseEnglish})
	codes := func(ls []Language) string {
		var s []string
		for _, l := range ls {
			s = append(s, l.Code)
		}
		return strings.Join(s, ",")
	}
	if got := codes(c.OutputLanguages(false, nil)); got != "en,de" {
		t.Errorf("shipping = %s", got)
	}
	if got := codes(c.OutputLanguages(true, nil)); got != "en,es,fr,de,qps" {
		t.Errorf("draft = %s", got)
	}
	if got := codes(c.OutputLanguages(true, []string{"fr"})); got != "en,fr,de,qps" {
		t.Errorf("draft fr = %s", got)
	}
}

func TestFilters(t *testing.T) {
	files := map[string]string{
		enFile("common"):       `{"common.a": {"text": "A ", "note": "n"}}`,
		enFile("web.photos"):   `{"web.photos.a": {"text": "B ", "note": "n"}}`,
		trFile("es", "common"): `{"common.a": {"text": "x\t", "en": "A "}}`,
		trFile("fr", "common"): `{"common.a": {"text": "y\t", "en": "A "}}`,
	}
	ds := check(t, files, CheckOptions{Prefixes: []string{"web"}})
	if len(find(ds, Error, "web.photos.a", "")) == 0 || len(find(ds, Error, "common.a", "")) > 0 {
		t.Errorf("-prefix web: %s", dump(ds))
	}
	ds = check(t, files, CheckOptions{Langs: []string{"es"}, Prefixes: []string{"common"}})
	for _, d := range ds {
		if d.Lang == "fr" || d.Prefix == "web.photos" {
			t.Errorf("filtered out: %s", d)
		}
	}
	want(t, ds, Error, "common.a", "tab character")       // Spanish
	want(t, ds, Error, "common.a", "trailing whitespace") // English, which every language depends on
}
