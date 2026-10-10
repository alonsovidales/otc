// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"strings"
	"testing"
)

func TestParseJSONRejectsDuplicatesAtAnyDepth(t *testing.T) {
	for name, src := range map[string]string{
		"top level":   "{\n\"a\": 1,\n\"a\": 2\n}",
		"nested":      "{\"x\": {\n\"text\": \"a\",\n\"text\": \"b\"}}",
		"plural form": "{\"x\": {\"text\": {\"one\": \"a\", \"other\": \"b\",\n\"one\": \"c\"}}}",
		"in an array": "[{\"k\": 1, \"k\": 2}]",
	} {
		_, err := parseJSON([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "duplicate key") {
			t.Errorf("%s: got %v, want a duplicate key error", name, err)
		}
	}
	_, err := parseJSON([]byte("{\n\"a\": 1,\n\n\"a\": 2\n}"))
	if je, ok := err.(*jsonError); !ok || je.line != 4 || !strings.Contains(je.msg, "first at line 2") {
		t.Errorf("duplicate reported as %v, want line 4 pointing at line 2", err)
	}
}

func TestParseJSONRejectsMalformedInput(t *testing.T) {
	for name, src := range map[string]string{
		"syntax":         "{\"a\": }",
		"trailing comma": "{\"a\": 1,}",
		"trailing data":  "{\"a\": 1} {\"b\": 2}",
		"bom":            "\xef\xbb\xbf{}",
		"invalid utf-8":  "{\"a\": \"\xff\"}",
		"truncated":      "{\"a\": [1, 2",
		"empty":          "",
	} {
		if _, err := parseJSON([]byte(src)); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
	if _, err := parseJSON([]byte("{\"a\": [1, {\"b\": null}], \"c\": true}\n")); err != nil {
		t.Errorf("valid JSON refused: %v", err)
	}
}

func TestDuplicateKeysInSourceFiles(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"): `{
  "common.a": {"text": "A", "note": "n"},
  "common.a": {"text": "B", "note": "n"}
}`,
		trFile("es", "common"): `{"common.a": {"text": "A", "en": "A", "text": "B"}}`,
	}, CheckOptions{})
	want(t, ds, Error, "", `duplicate key "common.a"`)
	want(t, ds, Error, "", `duplicate key "text"`)
}

func TestUnknownFieldsAndWrongTypes(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"): `{
  "common.a": {"text": "A", "note": "n", "txt": "typo"},
  "common.b": {"text": 3, "note": "n"},
  "common.c": {"text": "C", "note": "n", "max": -1},
  "common.d": {"text": "D", "note": "n", "max": 2.5},
  "common.e": {"text": "E", "note": "n", "stored": "yes"},
  "common.f": {"text": "F", "note": "n", "args": [["x"]]},
  "common.g": {"note": "n"},
  "common.h": "just a string"
}`,
		trFile("es", "common"): `{"common.a": {"text": "A", "en": "A", "note": "no notes here"}, "common.c": {"text": "C"}}`,
	}, CheckOptions{})
	want(t, ds, Error, "common.a", `unknown field "txt"`)
	want(t, ds, Error, "common.b", `must be a string or {"one"`)
	want(t, ds, Error, "common.c", `"max" must be a positive whole number`)
	want(t, ds, Error, "common.d", `"max" must be a positive whole number`)
	want(t, ds, Error, "common.e", `"stored" must be true or false`)
	want(t, ds, Error, "common.f", `["name", "type"] pair`)
	want(t, ds, Error, "common.g", `"text" is required`)
	want(t, ds, Error, "common.h", `must be an object`)
	want(t, ds, Error, "common.a", `unknown field "note" (an translation entry`)
	want(t, ds, Error, "common.c", `needs "text" and "en"`)
}

func TestLineNumbersPointAtTheEntry(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"): "{\n  \"common.a\": {\"text\": \"A\", \"note\": \"n\"},\n  \"common.b\": {\"text\": \"B \", \"note\": \"n\"}\n}\n",
	}, CheckOptions{})
	got := find(ds, Error, "common.b", "trailing whitespace")
	if len(got) != 1 || got[0].Line != 3 || got[0].File != enFile("common") {
		t.Fatalf("got %v, want one error at %s:3", got, enFile("common"))
	}
	if s := got[0].String(); s != "i18n/strings/en/common.json:3: error: common.b: trailing whitespace" {
		t.Errorf("String() = %q", s)
	}
}

func TestPrefixRules(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"):               `{"common.ok": {"text": "OK", "note": "n"}, "web.stray": {"text": "S", "note": "n"}}`,
		enFile("web.photos"):           `{"web.photos.title": {"text": "Photos", "note": "n"}, "web.files.title": {"text": "Files", "note": "n"}}`,
		enFile("web"):                  `{"web.misc": {"text": "Misc", "note": "n"}}`,
		enFile("nowhere"):              `{"nowhere.x": {"text": "X", "note": "n"}}`,
		enFile("a.b.c"):                `{}`,
		enFile("Bad"):                  `{}`,
		StringsDir + "/en/notes.txt":   "notes",
		trFile("es", "app.photos"):     `{}`,
		StringsDir + "/xx/common.json": `{}`,
	}, CheckOptions{})
	want(t, ds, Error, "web.stray", "belongs in i18n/strings/en/web.json")
	want(t, ds, Error, "web.files.title", "belongs in i18n/strings/en/web.files.json")
	want(t, ds, Error, "", `root "web" is split into files by area`)
	want(t, ds, Error, "", `root "nowhere" has no route`)
	want(t, ds, Error, "", `"a.b.c" is not a prefix`)
	want(t, ds, Error, "", `"Bad" is not a prefix`)
	want(t, ds, Error, "", "only <prefix>.json files belong")
	want(t, ds, Error, "", "there is no English i18n/strings/en/app.photos.json")
	want(t, ds, Error, "", `"xx" is not a language`)
	wantNone(t, ds, Error, "common.ok", "")
	wantNone(t, ds, Error, "web.photos.title", "")
}

func TestHiddenFilesAreIgnored(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"):                `{"common.ok": {"text": "OK", "note": "n"}}`,
		StringsDir + "/en/.DS_Store":    "\x00\x01",
		StringsDir + "/.DS_Store":       "\x00\x01",
		GlossaryDir + "/.DS_Store":      "\x00",
		trFile("es", "common") + ".swp": "x",
	}, CheckOptions{})
	want(t, ds, Error, "", "only <prefix>.json files belong in i18n/strings/es") // .json.swp is not hidden
	if n := len(find(ds, Error, "", "DS_Store")); n > 0 {
		t.Errorf("hidden files reported: %s", dump(ds))
	}
}

func TestTranslationEntryProblems(t *testing.T) {
	ds := check(t, map[string]string{
		enFile("common"):     `{"common.ok": {"text": "OK", "note": "n"}}`,
		enFile("web.photos"): `{"web.photos.title": {"text": "Photos", "note": "n"}}`,
		trFile("es", "common"): `{
  "common.gone": {"text": "Ido", "en": "Gone"},
  "web.photos.title": {"text": "Fotos", "en": "Photos"},
  "common.ok": {"text": {"one": "a", "other": "b"}, "en": "OK"}
}`,
	}, CheckOptions{})
	want(t, ds, Error, "common.gone", "there is no English entry")
	want(t, ds, Error, "web.photos.title", "belongs in i18n/strings/es/web.photos.json")
	want(t, ds, Error, "common.ok", `"text" and "en" must both be plural or both not`)
}
