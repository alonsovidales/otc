// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import (
	"encoding/json"
	"io/fs"
	"testing"
)

// The committed catalog: every language of languages_gen.go has its
// files, and every key in them is usable.
func TestEmbeddedCatalog(t *testing.T) {
	langs := Languages()
	if len(langs) == 0 || langs[0].Code != "en" || langs[0].Tag != "en" {
		t.Fatalf("Languages() = %v: English must come first", langs)
	}
	for i, l := range langs {
		files, _ := fs.Glob(catalogFS, "catalog/"+l.Code+"/*.json")
		if len(files) == 0 {
			t.Errorf("%s: no catalog files", l.Code)
		}
		keys := 0
		for _, f := range files {
			data, _ := fs.ReadFile(catalogFS, f)
			var m map[string]json.RawMessage
			if err := json.Unmarshal(data, &m); err != nil {
				t.Errorf("%s: %v", f, err)
			}
			delete(m, draftKey)
			keys += len(m)
		}
		if got := len(std.table(i).msgs); got != keys {
			t.Errorf("%s: %d of %d keys usable", l.Code, got, keys)
		}
		if Normalize(l.Code) != l.Code || Match(l.Tag) != l.Code {
			t.Errorf("%s: Normalize %q, Match %q", l.Code, Normalize(l.Code), Match(l.Tag))
		}
	}
	// Every English key renders as something other than its key.
	for key := range std.table(0).msgs {
		if T("en", key, nil) == key {
			t.Errorf("%s renders as its key", key)
		}
	}
}

func TestEmbeddedConstructors(t *testing.T) {
	if got := CommonCancel().Render("en"); got != "Cancel" {
		t.Errorf("CommonCancel() = %q", got)
	}
	if got := CommonConnecting().Render("xx"); got != "Connecting…" {
		t.Errorf("CommonConnecting() in an unknown language = %q", got)
	}
	if err := CommonSave().Check(); err != nil {
		t.Error(err)
	}
	m, err := ParseStored("common.ok", nil)
	if err != nil || m.Render("") != "OK" {
		t.Errorf("ParseStored: %v %v", m, err)
	}
	if got := RenderStored("en", "common.ok", nil, "Okay"); got != "Okay" {
		t.Errorf("RenderStored in English keeps the stored text: %q", got)
	}
	if got := FormatInt("en", 1234567); got != "1,234,567" {
		t.Errorf("FormatInt = %q", got)
	}
}
