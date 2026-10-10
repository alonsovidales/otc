// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBaselineRoundTrip(t *testing.T) {
	root := t.TempDir()
	b := &Baseline{Surface: "web", Files: map[string]int{"web/src/b.tsx": 2, "web/src/a.tsx": 5, "web/src/zero.tsx": 0}}
	if err := b.Write(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "i18n", "scan", "baseline", "web.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"surface\": \"web\",\n  \"total\": 7,\n  \"files\": {\n    \"web/src/a.tsx\": 5,\n    \"web/src/b.tsx\": 2\n  }\n}\n"
	if string(data) != want {
		t.Errorf("written:\n%s\nwant:\n%s", data, want)
	}
	got, ok, err := LoadBaseline(root, "web")
	if err != nil || !ok || got.Files["web/src/a.tsx"] != 5 || len(got.Files) != 2 {
		t.Fatalf("LoadBaseline = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := LoadBaseline(root, "ios"); ok || err != nil {
		t.Errorf("a missing baseline: ok %v, err %v", ok, err)
	}
}

func TestBaselineRefusesBadFiles(t *testing.T) {
	for name, content := range map[string]string{
		"unknown field": `{"surface": "web", "total": 1, "files": {}, "extra": 1}`,
		"zero count":    `{"surface": "web", "total": 0, "files": {"a.tsx": 0}}`,
		"wrong surface": `{"surface": "ios", "total": 0, "files": {}}`,
		"not json":      `{`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "i18n", "scan", "baseline")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "web.json"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := LoadBaseline(root, "web"); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestCompareAndUpdate(t *testing.T) {
	b := &Baseline{Surface: "web", Files: map[string]int{"a/x": 3, "a/y": 2, "b/z": 4, "a/gone": 1}}
	counts := map[string]int{"a/x": 4, "a/y": 1, "b/z": 4, "a/new": 2}
	c := Compare(b, counts, PathFilter(nil))
	if len(c.Rose) != 2 || c.Rose[0] != (Change{"a/new", 0, 2}) || c.Rose[1] != (Change{"a/x", 3, 4}) {
		t.Errorf("rose %+v", c.Rose)
	}
	if len(c.Fell) != 2 || c.Fell[0] != (Change{"a/gone", 1, 0}) || c.Fell[1] != (Change{"a/y", 2, 1}) {
		t.Errorf("fell %+v", c.Fell)
	}
	// only b/: nothing changed there
	if c := Compare(b, counts, PathFilter([]string{"./b/"})); len(c.Rose)+len(c.Fell) != 0 {
		t.Errorf("under b/: %+v", c)
	}
	// update a/y only: the rest stays as it was
	u := Updated(b, counts, PathFilter([]string{"a/y"}))
	if u.Files["a/y"] != 1 || u.Files["a/x"] != 3 || u.Files["a/gone"] != 1 || u.Files["a/new"] != 0 {
		t.Errorf("updated %+v", u.Files)
	}
	// update everything: files gone or at zero drop out
	u = Updated(b, map[string]int{"a/x": 1}, PathFilter(nil))
	if len(u.Files) != 1 || u.Files["a/x"] != 1 {
		t.Errorf("updated all %+v", u.Files)
	}
}

func TestPathFilter(t *testing.T) {
	keep := PathFilter([]string{"web/src/components", "app/ios/OffTheCloud/OffTheCloud/AppMenu.swift"})
	for rel, want := range map[string]bool{
		"web/src/components/A.tsx":                      true,
		"web/src/componentsX/A.tsx":                     false,
		"app/ios/OffTheCloud/OffTheCloud/AppMenu.swift": true,
		"web/src/App.tsx":                               false,
	} {
		if keep(rel) != want {
			t.Errorf("keep(%q) = %v", rel, !want)
		}
	}
}
