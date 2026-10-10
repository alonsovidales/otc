// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n/model"
)

const testLanguages = `{
  "languages": [
    {"code": "en", "tag": "en", "apple": "en", "android": "", "name": "English", "status": "shipping"},
    {"code": "es", "tag": "es", "apple": "es", "android": "es", "name": "Español", "status": "draft"}
  ],
  "routes": {"common": ["web", "ios"], "web": ["web"], "dev": ["go"]}
}
`

const testCommon = `{
  "common.save": {
    "text": "Save",
    "note": "Button"
  }
}
`

const testWeb = `{
  "web.title": {
    "text": "Photos",
    "note": "Page title"
  }
}
`

// fakeEmitter writes out/<code>/<prefix>.txt with one "key=text" line per
// key of the web consumer, which is enough to drive the generate and check
// plumbing the real emitters go through.
type fakeEmitter struct {
	name     string
	noMarker bool              // forget DraftMarker in draft mode
	extra    map[string][]byte // more files to return
	err      error
}

func (f fakeEmitter) Name() string        { return f.name }
func (f fakeEmitter) Consumers() []string { return []string{model.ConsumerWeb} }
func (f fakeEmitter) Owns() []string      { return []string{"out/*/*.txt"} }

func (f fakeEmitter) Emit(c *model.Catalog, opts Options) (map[string][]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string][]byte{}
	for _, l := range opts.Languages {
		for _, p := range c.PrefixesFor(model.ConsumerWeb) {
			var b bytes.Buffer
			if opts.Draft && !f.noMarker {
				b.WriteString("# " + DraftMarker + "\n")
			}
			for _, r := range c.Messages(l, p) {
				fmt.Fprintf(&b, "%s=%s (%s)\n", r.Key, r.Text.Other, r.State)
			}
			out["out/"+l.Code+"/"+p+".txt"] = b.Bytes()
		}
	}
	for p, data := range f.extra {
		out[p] = data
	}
	return out, nil
}

// withEmitters replaces the registry for one test.
func withEmitters(t *testing.T, es ...Emitter) {
	t.Helper()
	saved := registry
	registry = map[string]Emitter{}
	for _, e := range es {
		register(e)
	}
	t.Cleanup(func() { registry = saved })
}

// newRepo writes a minimal repository and returns its root.
func newRepo(t *testing.T) string {
	t.Helper()
	return writeRepo(t, map[string]string{
		model.LanguagesFile:           testLanguages,
		"i18n/strings/en/common.json": testCommon,
		"i18n/strings/en/web.json":    testWeb,
	})
}

// writeRepo writes a repository from path -> content and returns its root.
// Emitter tests use it to build catalogs, hostile ones included, and call
// model.Load and Emit on them directly.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		write(t, dir, rel, content)
	}
	return dir
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// gen runs i18ngen on root and returns its exit code and output.
func gen(t *testing.T, root string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(append([]string{"-root", root}, args...), &out, &out)
	return code, out.String()
}

func mustRun(t *testing.T, root string, wantCode int, wantOut string, args ...string) string {
	t.Helper()
	code, out := gen(t, root, args...)
	if code != wantCode || !strings.Contains(out, wantOut) {
		t.Fatalf("i18ngen %v: exit %d, want %d with %q; output:\n%s", args, code, wantCode, wantOut, out)
	}
	return out
}

func TestGenerateThenCheck(t *testing.T) {
	withEmitters(t, fakeEmitter{name: "fake"})
	root := newRepo(t)
	mustRun(t, root, 0, "3 generated files written") // en x {common, web}, and stored.json
	if got := read(t, root, "out/en/web.txt"); got != "web.title=Photos (source)\n" {
		t.Errorf("out/en/web.txt = %q", got)
	}
	if exists(root, "out/es/web.txt") {
		t.Error("draft languages must not be generated without -draft")
	}
	if got := read(t, root, model.StoredFile); got != "{}\n" {
		t.Errorf("%s = %q", model.StoredFile, got)
	}
	mustRun(t, root, 0, "generated files checked", "-check")
	mustRun(t, root, 0, "0 generated files written") // nothing changed

	// Reworded English without regenerating: the committed output is stale.
	write(t, root, "i18n/strings/en/web.json", strings.Replace(testWeb, "Photos", "Pictures", 1))
	mustRun(t, root, 1, "out/en/web.txt: error: generated file is out of date: run make i18n", "-check")
	mustRun(t, root, 0, "1 generated file written")
	mustRun(t, root, 0, "", "-check")

	// A generated file edited by hand, deleted, or left over.
	write(t, root, "out/en/web.txt", "web.title=Hacked\n")
	mustRun(t, root, 1, "out/en/web.txt: error: generated file is out of date", "-check")
	os.Remove(filepath.Join(root, "out/en/web.txt"))
	mustRun(t, root, 1, "out/en/web.txt: error: generated file is missing", "-check")
	mustRun(t, root, 0, "")
	write(t, root, "out/en/gone.txt", "old prefix\n")
	mustRun(t, root, 1, "out/en/gone.txt: error: no longer generated (by fake)", "-check")
	mustRun(t, root, 0, "1 removed")
	if exists(root, "out/en/gone.txt") {
		t.Error("generate must delete owned files it no longer produces")
	}

	// A new stored key changes the stored-key record.
	write(t, root, "i18n/strings/en/dev.json", `{
  "dev.alert": {
    "text": "Alert",
    "note": "An alert",
    "stored": true
  }
}
`)
	mustRun(t, root, 1, model.StoredFile+": error: generated file is out of date", "-check")
	mustRun(t, root, 0, "")
	if got := read(t, root, model.StoredFile); !strings.Contains(got, `"dev.alert": []`) {
		t.Errorf("%s = %q", model.StoredFile, got)
	}
	// ... and then the key can't be deleted.
	os.Remove(filepath.Join(root, "i18n/strings/en/dev.json"))
	mustRun(t, root, 1, "a stored key was deleted")
}

func TestDraftOutputIsNeverCommitted(t *testing.T) {
	withEmitters(t, fakeEmitter{name: "fake"})
	root := newRepo(t)
	write(t, root, "i18n/strings/es/web.json", `{
  "web.title": {
    "text": "Fotos",
    "en": "Photos"
  }
}
`)
	mustRun(t, root, 0, "DRAFT output", "-draft")
	es := read(t, root, "out/es/web.txt")
	if !strings.Contains(es, DraftMarker) || !strings.Contains(es, "web.title=Fotos (translated)") {
		t.Errorf("out/es/web.txt = %q", es)
	}
	if got := read(t, root, "out/qps/web.txt"); !strings.Contains(got, "web.title=[Phótóš ~~] (pseudo)") {
		t.Errorf("pseudo-locale = %q", got)
	}
	if got := read(t, root, "out/en/web.txt"); !strings.Contains(got, DraftMarker) {
		t.Error("every file written in draft mode carries the marker")
	}
	out := mustRun(t, root, 1, "holds draft output (make i18n DRAFT=1)", "-check")
	if !strings.Contains(out, "out/es/web.txt: error: holds draft output") || !strings.Contains(out, "out/en/web.txt: error: holds draft output") {
		t.Errorf("every draft file is reported:\n%s", out)
	}
	mustRun(t, root, 0, "removed")
	if exists(root, "out/es/web.txt") || exists(root, "out/qps/web.txt") {
		t.Error("a plain run removes the draft languages' files")
	}
	mustRun(t, root, 0, "", "-check")

	// -lang picks the draft languages to write.
	write(t, root, model.LanguagesFile, strings.Replace(testLanguages, `"status": "draft"}`, `"status": "draft"},
    {"code": "fr", "tag": "fr", "apple": "fr", "android": "fr", "name": "Français", "status": "draft"}`, 1))
	mustRun(t, root, 0, "", "-draft", "-lang", "fr")
	if exists(root, "out/es/web.txt") || !exists(root, "out/fr/web.txt") {
		t.Error("-draft -lang fr writes French only")
	}
}

func TestDraftMarkerIsEnforced(t *testing.T) {
	withEmitters(t, fakeEmitter{name: "forgetful", noMarker: true})
	root := newRepo(t)
	mustRun(t, root, 1, "emitter forgetful wrote draft output without DraftMarker", "-draft")
	if exists(root, "out/en/web.txt") {
		t.Error("nothing is written when an emitter fails")
	}
}

func TestSourcesAreCanonicalized(t *testing.T) {
	withEmitters(t)
	root := newRepo(t)
	messy := `{"web.title": {"note": "Page title", "text": "Photos"}, "web.about": {"text": "About", "note": "Link"}}`
	write(t, root, "i18n/strings/en/web.json", messy)
	mustRun(t, root, 1, "i18n/strings/en/web.json: error: not in canonical form", "-check")
	if read(t, root, "i18n/strings/en/web.json") != messy {
		t.Fatal("-check must not write")
	}
	mustRun(t, root, 0, "1 source file rewritten")
	got := read(t, root, "i18n/strings/en/web.json")
	if !strings.HasPrefix(got, "{\n  \"web.about\": {\n    \"text\": \"About\",\n    \"note\": \"Link\"\n  },\n  \"web.title\"") {
		t.Errorf("rewritten source:\n%s", got)
	}
	mustRun(t, root, 0, "", "-check")
}

func TestErrorsStopGeneration(t *testing.T) {
	withEmitters(t, fakeEmitter{name: "fake"})
	root := newRepo(t)
	write(t, root, "i18n/strings/en/web.json", "{\n  \"web.title\": {\n    \"text\": \"Photos \",\n    \"note\": \"Page title\"\n  }\n}\n")
	out := mustRun(t, root, 1, "i18n/strings/en/web.json:2: error: web.title: trailing whitespace")
	if !strings.Contains(out, "1 error,") {
		t.Errorf("summary missing:\n%s", out)
	}
	if exists(root, "out/en/web.txt") || exists(root, model.StoredFile) {
		t.Error("nothing is generated from a catalog with errors")
	}
	write(t, root, "i18n/strings/en/web.json", `{"web.title": {"text": "Photos", "note": "Page title", "text": "Again"}}`)
	mustRun(t, root, 1, `duplicate key "text"`)
	if got := read(t, root, "i18n/strings/en/web.json"); !strings.Contains(got, `"text": "Again"`) {
		t.Error("a file that can't be read whole is never rewritten")
	}
}

func TestReleaseAndFilters(t *testing.T) {
	withEmitters(t, fakeEmitter{name: "fake"})
	root := newRepo(t)
	mustRun(t, root, 0, "")
	// Spanish is a draft: missing text is a note, until a release check
	// names it.
	out := mustRun(t, root, 0, "(3 notes, -v shows them)", "-check") // 2 missing, 1 consumers without an emitter
	if strings.Contains(out, "not translated") {
		t.Errorf("notes are hidden without -v:\n%s", out)
	}
	mustRun(t, root, 0, "i18n/strings/es/web.json: info: web.title: not translated", "-check", "-v")
	mustRun(t, root, 0, "es (draft): 0/2 translated, 2 missing", "-check", "-v")
	mustRun(t, root, 1, "i18n/strings/es/web.json: error: web.title: not translated", "-check", "-release", "-lang", "es")
	out = mustRun(t, root, 1, "web.title: not translated", "-check", "-release", "-lang", "es", "-prefix", "web")
	if strings.Contains(out, "common.save") {
		t.Errorf("-prefix web hides common:\n%s", out)
	}
	// A narrowed check doesn't compare generated files (that is the full
	// check's job), so a translator isn't stopped by someone else's change.
	write(t, root, "out/en/web.txt", "stale\n")
	mustRun(t, root, 0, "generated files not compared", "-check", "-lang", "es")
	mustRun(t, root, 1, "out of date", "-check")
}

func TestEmitterOutputsAreValidated(t *testing.T) {
	root := newRepo(t)
	for name, tc := range map[string]struct {
		emitters []Emitter
		msg      string
	}{
		"outside":   {[]Emitter{fakeEmitter{name: "a", extra: map[string][]byte{"../x": nil}}}, `"../x" is not a clean path`},
		"absolute":  {[]Emitter{fakeEmitter{name: "a", extra: map[string][]byte{"/etc/x": nil}}}, "not a relative"},
		"source":    {[]Emitter{fakeEmitter{name: "a", extra: map[string][]byte{"i18n/strings/en/x.json": nil}}}, "is a catalog source"},
		"stored":    {[]Emitter{fakeEmitter{name: "a", extra: map[string][]byte{model.StoredFile: nil}}}, "is a catalog source"},
		"twice":     {[]Emitter{fakeEmitter{name: "a"}, fakeEmitter{name: "b"}}, "generated by both a and b"},
		"emit fail": {[]Emitter{fakeEmitter{name: "a", err: errors.New("boom")}}, "emitter a: boom"},
	} {
		t.Run(name, func(t *testing.T) {
			withEmitters(t, tc.emitters...)
			mustRun(t, root, 1, tc.msg)
		})
	}
}

func TestUsage(t *testing.T) {
	withEmitters(t)
	root := newRepo(t)
	for _, args := range [][]string{
		{"-check", "-draft"},
		{"-prefix", "web"},
		{"-lang", "es"},
		{"-check", "-lang", "en"},
		{"-check", "-lang", "xx"},
		{"-check", "-lang", "qps"},
		{"-nope"},
		{"extra"},
	} {
		if code, out := gen(t, root, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2:\n%s", args, code, out)
		}
	}
	if code, out := gen(t, t.TempDir()); code != 1 || !strings.Contains(out, "languages.json") {
		t.Errorf("a root without languages.json: exit %d:\n%s", code, out)
	}
}

func TestRegisterRefusesBadEmitters(t *testing.T) {
	withEmitters(t, fakeEmitter{name: "a"})
	for name, e := range map[string]Emitter{
		"duplicate":        fakeEmitter{name: "a"},
		"unknown consumer": badConsumer{fakeEmitter{name: "b"}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: register accepted it", name)
				}
			}()
			register(e)
		}()
	}
}

type badConsumer struct{ fakeEmitter }

func (badConsumer) Consumers() []string { return []string{"tv"} }

func TestFindRoot(t *testing.T) {
	root := newRepo(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	got, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(root); mustEval(t, got) != want {
		t.Errorf("findRoot() = %s, want %s", got, want)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
