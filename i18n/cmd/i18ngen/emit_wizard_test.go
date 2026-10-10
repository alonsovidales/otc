// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n/model"
)

// wizardSample stands in for setup_wizard.py: code on both sides of the
// block, which must come out byte for byte.
const wizardSample = `#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""A wizard."""

import json

PAGE = r"""<!doctype html><p>{{.}} $& %s</p>"""

# BEGIN GENERATED I18N
I18N = {"stale": "replaced"}
# END GENERATED I18N


def t(code, key):
    return I18N.get(code, I18N["en"]).get(key, key)
`

// wizardCheck is what the design asks of the block (docs/i18n.md): the
// whole file parses, the block is a single assignment of a dict literal
// made of strings and dicts only, and Python reads it as the same value
// as JSON does. It prints that value as JSON.
const wizardCheck = `
import ast, json, sys
src = open(sys.argv[1], encoding="utf-8").read()
ast.parse(src)
lines = src.split("\n")
b, e = lines.index("# BEGIN GENERATED I18N"), lines.index("# END GENERATED I18N")
assert lines.count("# BEGIN GENERATED I18N") == 1 and lines.count("# END GENERATED I18N") == 1
block = "\n".join(lines[b + 1:e])
mod = ast.parse(block)
assert len(mod.body) == 1, "one statement"
node = mod.body[0]
assert isinstance(node, ast.Assign) and len(node.targets) == 1
assert isinstance(node.targets[0], ast.Name) and node.targets[0].id == "I18N"
for n in ast.walk(node.value):
    assert isinstance(n, (ast.Dict, ast.Constant)), type(n).__name__
    if isinstance(n, ast.Constant):
        assert isinstance(n.value, str)
value = ast.literal_eval(node.value)
lit = block[block.index("\nI18N = ") + len("\nI18N = "):] if not block.startswith("I18N = ") else block[len("I18N = "):]
assert json.loads(lit) == value, "JSON and Python disagree"
sys.stdout.write(json.dumps(value, ensure_ascii=True, sort_keys=True))
`

// wizardPythonReads runs wizardCheck on a wizard file and returns the value of
// I18N; it skips the test without python3.
func wizardPythonReads(t *testing.T, src []byte) map[string]map[string]any {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	file := filepath.Join(t.TempDir(), "setup_wizard.py")
	if err := os.WriteFile(file, src, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(py, "-I", "-c", wizardCheck, file).CombinedOutput()
	if err != nil {
		t.Fatalf("python3: %v\n%s\n--- file:\n%s", err, out, src)
	}
	var v map[string]map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return v
}

func wizardEmit(t *testing.T, root string, draft bool) []byte {
	t.Helper()
	cat, err := model.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := wizardEmitter{}.Emit(cat, Options{Root: root, Draft: draft, Languages: cat.OutputLanguages(draft, nil)})
	if err != nil {
		t.Fatal(err)
	}
	return out[wizardFile]
}

// wizardOutside returns src without the block's content.
func wizardOutside(t *testing.T, src []byte) string {
	t.Helper()
	s := string(src)
	b := strings.Index(s, wizardBegin+"\n")
	e := strings.Index(s, "\n"+wizardEnd+"\n")
	if b < 0 || e < b {
		t.Fatalf("markers lost:\n%s", s)
	}
	return s[:b+len(wizardBegin)+1] + s[e+1:]
}

func TestWizardBlock(t *testing.T) {
	root := writeRepo(t, map[string]string{
		model.LanguagesFile:           goHostileLanguages,
		"i18n/strings/en/common.json": `{"common.ok": {"text": "OK", "note": "n"}}`,
		"i18n/strings/en/wiz.json":    `{"wiz.files": {"text": {"one": "{count} file", "other": "{count} files"}, "args": [["count", "count"]], "note": "n"}, "wiz.hi": {"text": "Hi {name}", "args": [["name", "user"]], "note": "n"}}`,
		"i18n/strings/es/wiz.json":    `{"wiz.files": {"text": {"one": "{count} archivo", "other": "{count} archivos"}, "en": {"one": "{count} file", "other": "{count} files"}}}`,
		"i18n/strings/en/dev.json":    `{"dev.x": {"text": "Not for the wizard", "note": "n"}}`,
		filepath.ToSlash(wizardFile):  wizardSample,
		"i18n/strings/es/common.json": `{"common.ok": {"text": "Vale", "en": "OK"}}`,
		"i18n/strings/en/site.json":   `{"site.y": {"text": "Not either", "note": "n"}}`,
		"i18n/strings/es/dev.json":    `{"dev.x": {"text": "No", "en": "Not for the wizard"}}`,
	})
	src := wizardEmit(t, root, false)
	want := map[string]map[string]any{
		"en": {"common.ok": "OK", "wiz.files": map[string]any{"one": "{count} file", "other": "{count} files"}, "wiz.hi": "Hi {name}"},
		"es": {"common.ok": "Vale", "wiz.files": map[string]any{"one": "{count} archivo", "other": "{count} archivos"}}, // wiz.hi: English
	}
	if got := wizardPythonReads(t, src); !reflect.DeepEqual(got, want) {
		t.Errorf("Python reads %v, want %v", got, want)
	}
	if got, want := wizardOutside(t, src), wizardOutside(t, []byte(wizardSample)); got != want {
		t.Errorf("the rest of the file changed:\n%s", got)
	}
	// Generating again from the output changes nothing.
	if err := os.WriteFile(filepath.Join(root, wizardFile), src, 0o644); err != nil {
		t.Fatal(err)
	}
	if again := wizardEmit(t, root, false); !bytes.Equal(again, src) {
		t.Errorf("a second run differs:\n%s", again)
	}
	if draft := wizardEmit(t, root, true); !bytes.Contains(draft, []byte("# "+DraftMarker)) {
		t.Errorf("draft output lacks the marker:\n%s", draft)
	} else if v := wizardPythonReads(t, draft); v["qps"] == nil {
		t.Errorf("draft output lacks the pseudo-locale: %v", v)
	}
}

// Hostile text is one more string in the dict, whatever it means to
// Python, JSON or HTML, and the rest of the file is untouched.
func TestWizardHostileCatalog(t *testing.T) {
	root := goHostileRepo(t, "dev", nil)
	src := wizardEmit(t, root, false)
	got := wizardPythonReads(t, src)
	for code, n := range map[string]int{"en": len(goHostileTexts) + 1, "es": len(goHostileTexts)} { // common.ok is English only
		if len(got[code]) != n {
			t.Errorf("%s has %d texts, want %d", code, len(got[code]), n)
		}
		for i, s := range goHostileTexts {
			if v := got[code]["wiz.h"+goHostileID(i)]; v != s {
				t.Errorf("%s wiz.h%s = %q, want %q", code, goHostileID(i), v, s)
			}
		}
	}
	if got, want := wizardOutside(t, src), wizardOutside(t, []byte(wizardSample)); got != want {
		t.Errorf("the rest of the file changed:\n%s", got)
	}
	// Markup can't appear in the literal at all: < > & are escaped.
	lit := src[bytes.Index(src, []byte("\nI18N = ")):bytes.Index(src, []byte(wizardEnd))]
	if bytes.ContainsAny(lit, "<>&") {
		t.Errorf("the literal holds raw markup characters:\n%s", lit)
	}
}

func TestWizardRefuses(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no markers":   {wizardFile: "print(1)\n"},
		"end first":    {wizardFile: wizardEnd + "\n" + wizardBegin + "\n"},
		"two begins":   {wizardFile: wizardBegin + "\n" + wizardBegin + "\n" + wizardEnd + "\n"},
		"two ends":     {wizardFile: wizardBegin + "\n" + wizardEnd + "\n" + wizardEnd + "\n"},
		"indented":     {wizardFile: "  " + wizardBegin + "\n  " + wizardEnd + "\n"},
		"markup":       {"i18n/strings/en/wiz.json": `{"wiz.x": {"text": "</script><img onerror=alert(1)>", "note": "n"}}`},
		"template":     {"i18n/strings/en/wiz.json": `{"wiz.x": {"text": "{{.}}", "note": "n"}}`},
		"undeclared":   {"i18n/strings/en/wiz.json": `{"wiz.x": {"text": "{name}", "note": "n"}}`},
		"unlisted tag": {"i18n/strings/en/wiz.json": `{"wiz.x": {"text": "<b>x</b>", "note": "n"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			repo := map[string]string{model.LanguagesFile: goHostileLanguages, wizardFile: wizardSample}
			for k, v := range files {
				repo[k] = v
			}
			root := writeRepo(t, repo)
			cat, err := model.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if out, err := (wizardEmitter{}).Emit(cat, Options{Root: root, Languages: cat.OutputLanguages(false, nil)}); err == nil {
				t.Errorf("emitted instead of refusing:\n%s", out[wizardFile])
			}
		})
	}
	// A repository without the wizard has nothing to edit.
	root := writeRepo(t, map[string]string{model.LanguagesFile: goHostileLanguages})
	cat, _ := model.Load(root)
	if out, err := (wizardEmitter{}).Emit(cat, Options{Root: root, Languages: cat.OutputLanguages(false, nil)}); err != nil || len(out) != 0 {
		t.Errorf("no wizard: %v %v", out, err)
	}
}

// The committed setup_wizard.py passes the same check.
func TestWizardCommittedBlock(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("../../..", wizardFile))
	if err != nil {
		t.Fatal(err)
	}
	if v := wizardPythonReads(t, src); v[model.SourceCode]["common.cancel"] != "Cancel" {
		t.Errorf("the committed block lacks English: %v", v)
	}
}
