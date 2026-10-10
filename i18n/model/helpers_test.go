// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testLanguages is languages.json for the tests: the real routes, English
// and German shipping (so the shipping rules have a translated language to
// apply to), Spanish and French drafts.
const testLanguages = `{
  "languages": [
    {"code": "en", "tag": "en", "apple": "en", "android": "", "name": "English", "status": "shipping"},
    {"code": "es", "tag": "es", "apple": "es", "android": "es", "name": "Español", "status": "draft"},
    {"code": "fr", "tag": "fr", "apple": "fr", "android": "fr", "name": "Français", "status": "draft"},
    {"code": "de", "tag": "de", "apple": "de", "android": "de", "name": "Deutsch", "status": "shipping"}
  ],
  "routes": {
    "common": ["web", "ios", "android", "macos", "otcsync", "wizard"],
    "web": ["web"], "app": ["ios", "android"], "ios": ["ios"], "android": ["android"],
    "desk": ["macos", "otcsync"], "macos": ["macos"],
    "dev": ["go"], "push": ["go"], "mail": ["go"], "api": ["go"], "site": ["go"], "wiz": ["wizard"]
  }
}`

// writeRepo creates a repository holding files (path -> content) plus
// testLanguages unless files has its own languages.json.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files[LanguagesFile]; !ok {
		files[LanguagesFile] = testLanguages
	}
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func load(t *testing.T, files map[string]string) *Catalog {
	t.Helper()
	c, err := Load(writeRepo(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// check loads files and returns the check's diagnostics.
func check(t *testing.T, files map[string]string, opts CheckOptions) []Diagnostic {
	t.Helper()
	return load(t, files).Check(opts)
}

// enFile is the path of an English source file.
func enFile(prefix string) string { return StringsDir + "/en/" + prefix + ".json" }

// trFile is the path of a translation file.
func trFile(code, prefix string) string { return StringsDir + "/" + code + "/" + prefix + ".json" }

// find returns the diagnostics of a severity whose key and message match.
func find(ds []Diagnostic, sev Severity, key, msg string) []Diagnostic {
	var out []Diagnostic
	for _, d := range ds {
		if d.Severity == sev && (key == "" || d.Key == key) && strings.Contains(d.Msg, msg) {
			out = append(out, d)
		}
	}
	return out
}

func want(t *testing.T, ds []Diagnostic, sev Severity, key, msg string) {
	t.Helper()
	if len(find(ds, sev, key, msg)) == 0 {
		t.Errorf("no %s for %q containing %q; got:\n%s", sev, key, msg, dump(ds))
	}
}

func wantNone(t *testing.T, ds []Diagnostic, sev Severity, key, msg string) {
	t.Helper()
	if got := find(ds, sev, key, msg); len(got) > 0 {
		t.Errorf("unexpected %s for %q containing %q:\n%s", sev, key, msg, dump(got))
	}
}

// wantClean fails on any error or warning.
func wantClean(t *testing.T, ds []Diagnostic) {
	t.Helper()
	var bad []Diagnostic
	for _, d := range ds {
		if d.Severity >= Warning {
			bad = append(bad, d)
		}
	}
	if len(bad) > 0 {
		t.Errorf("expected no errors or warnings, got:\n%s", dump(bad))
	}
}

// wantNoErrors fails on any error.
func wantNoErrors(t *testing.T, ds []Diagnostic) {
	t.Helper()
	if errs := find(ds, Error, "", ""); len(errs) > 0 {
		t.Errorf("expected no errors, got:\n%s", dump(errs))
	}
}

// js is s as a JSON string.
func js(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func dump(ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString("  " + d.String() + "\n")
	}
	return b.String()
}
