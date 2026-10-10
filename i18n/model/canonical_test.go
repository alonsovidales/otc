// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"testing"
)

func TestCanonicalForm(t *testing.T) {
	messy := `{"common.z": {"note": "Last", "text": "Z"},
"common.a": {"translate": true, "stored": false, "rich": [], "max": 20, "note": "First <b>note</b> & more", "args": [["name","user"],["n","count"]],
  "text": {"other": "{name} has {n} <b>photos</b>", "one": "{name} has a <b>photo</b>"}, "rich": ["b"]},
"common.m": {"text": "Middle \"quoted\"\nline", "note": "n", "stored": true, "review": "required", "reviewed": "owner 2026-10-12", "translate": false}}`
	// "rich" twice is a duplicate key; drop the first for the round trip.
	messy = replaceOnce(messy, `"rich": [], `, "")
	c := load(t, map[string]string{enFile("common"): messy})
	f := c.Files()[0]
	if f.Broken {
		t.Fatalf("messy but valid file reported broken: %s", dump(c.Check(CheckOptions{})))
	}
	got := string(f.Canonical())
	wantText := `{
  "common.a": {
    "text": {
      "one": "{name} has a <b>photo</b>",
      "other": "{name} has {n} <b>photos</b>"
    },
    "args": [["name", "user"], ["n", "count"]],
    "note": "First <b>note</b> & more",
    "max": 20,
    "rich": ["b"]
  },
  "common.m": {
    "text": "Middle \"quoted\"\nline",
    "note": "n",
    "stored": true,
    "review": "required",
    "reviewed": "owner 2026-10-12 #` + Fingerprint(PlainText("Middle \"quoted\"\nline")) + `",
    "translate": false
  },
  "common.z": {
    "text": "Z",
    "note": "Last"
  }
}
`
	if got != wantText {
		t.Fatalf("canonical form:\n%s\nwant:\n%s", got, wantText)
	}
	if len(c.NonCanonical(CheckOptions{})) != 1 {
		t.Error("the messy file should be reported as not canonical")
	}

	// Round trip: the canonical form reads back to the same entries and
	// is its own canonical form.
	c2 := load(t, map[string]string{enFile("common"): got})
	if n := len(c2.NonCanonical(CheckOptions{})); n != 0 {
		t.Errorf("canonical form is not stable: %s", dump(c2.NonCanonical(CheckOptions{})))
	}
	for _, k := range []string{"common.a", "common.m", "common.z"} {
		a, b := c.Entry(k), c2.Entry(k)
		if a.Text != b.Text || a.Note != b.Note || a.Max != b.Max || a.Stored != b.Stored || a.Translate != b.Translate || len(a.Args) != len(b.Args) || len(a.Rich) != len(b.Rich) {
			t.Errorf("%s changed in the round trip: %+v -> %+v", k, a, b)
		}
	}
}

func TestCanonicalTranslations(t *testing.T) {
	c := load(t, map[string]string{
		enFile("common"):       `{"common.a": {"text": "A", "note": "n"}, "common.b": {"text": {"one": "a b", "other": "{n} bs"}, "args": [["n", "count"]], "note": "n"}}`,
		trFile("es", "common"): `{"common.b": {"en": {"other": "{n} bs", "one": "a b"}, "text": {"one": "una b", "other": "{n} bes"}}, "common.a": {"en": "A", "text": "Á"}}`,
	})
	var es *SourceFile
	for _, f := range c.Files() {
		if f.Lang == "es" {
			es = f
		}
	}
	want := `{
  "common.a": {
    "text": "Á",
    "en": "A"
  },
  "common.b": {
    "text": {
      "one": "una b",
      "other": "{n} bes"
    },
    "en": {
      "one": "a b",
      "other": "{n} bs"
    }
  }
}
`
	if got := string(es.Canonical()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	empty := load(t, map[string]string{enFile("common"): "{\n}"})
	if got := string(empty.Files()[0].Canonical()); got != "{}\n" {
		t.Errorf("empty file: %q", got)
	}
}

func TestBrokenFilesAreNotCanonicalized(t *testing.T) {
	c := load(t, map[string]string{enFile("common"): `{"common.a": {"text": "A", "note": "n", "oops": 1}}`})
	if !c.Files()[0].Broken {
		t.Fatal("an unknown field must mark the file broken")
	}
	if len(c.NonCanonical(CheckOptions{})) != 0 {
		t.Error("a broken file is reported by its own error, not as non-canonical")
	}
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
