// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n/model"
)

var updateWebGolden = flag.Bool("update-web-golden", false, "rewrite testdata/web from the web emitter's output")

// webTestLanguages ships Spanish too, so that the golden files show a
// translation, a stale one and a missing one.
const webTestLanguages = `{
  "languages": [
    {"code": "en", "tag": "en", "apple": "en", "android": "", "name": "English", "status": "shipping"},
    {"code": "es", "tag": "es", "apple": "es", "android": "es", "name": "Español", "status": "shipping"},
    {"code": "fr", "tag": "fr", "apple": "fr", "android": "fr", "name": "Français", "status": "draft"},
    {"code": "pt", "tag": "pt-PT", "apple": "pt-PT", "android": "pt", "name": "Português", "status": "draft"}
  ],
  "routes": {"common": ["ios", "web"], "web": ["web"], "app": ["ios"], "dev": ["go"]}
}
`

// webTestRepo is a catalog with every shape the web emitter writes: plain
// and plural texts, user and text arguments, snake_case names, tags, and the
// characters JSON and HTML care about. app and dev aren't routed to the web.
func webTestRepo(t *testing.T) string {
	t.Helper()
	return writeRepo(t, map[string]string{
		model.LanguagesFile: webTestLanguages,
		"i18n/strings/en/common.json": `{
  "common.cancel": {
    "text": "Cancel",
    "note": "Button"
  },
  "common.save": {
    "text": "Save",
    "note": "Button"
  }
}
`,
		"i18n/strings/en/web.files.json": `{
  "web.files.deleted": {
    "text": {
      "one": "{name} deleted a file",
      "other": "{name} deleted {files} files"
    },
    "args": [["name", "user"], ["files", "count"]],
    "note": "Alert line"
  },
  "web.files.escapes": {
    "text": "Quotes \" and ', a backslash in C:\\Users\\Ana, 100% & more > less, $& and $1, %s%n, Español… 😀 {size}\nSecond line",
    "args": [["size", "int"]],
    "note": "Every character an output format escapes"
  },
  "web.files.renamed": {
    "text": "{file_name} is now {new_name}",
    "args": [["file_name", "user"], ["new_name", "text"]],
    "note": "Toast after a rename"
  },
  "web.files.terms": {
    "text": "Read the <link>terms</link> before <b>{name}</b> signs",
    "args": [["name", "user"]],
    "note": "Line above the sign-up button",
    "rich": ["link", "b"]
  }
}
`,
		"i18n/strings/en/web.photos.json": `{
  "web.photos.title": {
    "text": "Photos",
    "note": "Page title"
  }
}
`,
		"i18n/strings/en/app.json": `{
  "app.title": {
    "text": "Not for the web",
    "note": "iOS only"
  }
}
`,
		"i18n/strings/en/dev.json": `{
  "dev.size": {
    "text": "Uses {size}",
    "args": [["size", "bytes"]],
    "note": "Go only"
  }
}
`,
		"i18n/strings/es/common.json": `{
  "common.save": {
    "text": "Guardar",
    "en": "Save"
  }
}
`,
		"i18n/strings/es/web.files.json": `{
  "web.files.deleted": {
    "text": {
      "one": "{name} borró un archivo",
      "other": "{name} borró {files} archivos"
    },
    "en": {
      "one": "{name} deleted a file",
      "other": "{name} deleted {files} files"
    }
  },
  "web.files.renamed": {
    "text": "{file_name} ahora se llama {new_name}",
    "en": "{file_name} is called {new_name} now"
  },
  "web.files.terms": {
    "text": "Lee los <link>términos</link> antes de que <b>{name}</b> firme",
    "en": "Read the <link>terms</link> before <b>{name}</b> signs"
  }
}
`,
	})
}

// loadChecked loads a repository and fails on any error Check finds, as
// the driver would.
func loadChecked(t *testing.T, root string) *model.Catalog {
	t.Helper()
	c, err := model.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if ds := c.Check(model.CheckOptions{}); model.Count(ds, model.Error) > 0 {
		for _, d := range ds {
			t.Log(d.String())
		}
		t.Fatal("the test catalog has errors")
	}
	return c
}

func emitWeb(t *testing.T, c *model.Catalog, draft bool) map[string][]byte {
	t.Helper()
	out, err := webEmitter{}.Emit(c, Options{Draft: draft, Languages: c.OutputLanguages(draft, nil)})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestWebGolden compares every file with testdata/web (go test -run
// TestWebGolden -update-web-golden rewrites it). The golden files are the
// reference for the formats the web runtime reads.
func TestWebGolden(t *testing.T) {
	out := emitWeb(t, loadChecked(t, webTestRepo(t)), false)
	golden := filepath.Join("testdata", "web")
	want := map[string]bool{}
	for p := range out {
		rel, ok := strings.CutPrefix(p, "web/src/i18n/")
		if !ok {
			t.Fatalf("%s is outside web/src/i18n", p)
		}
		want[rel] = true
		gp := filepath.Join(golden, filepath.FromSlash(rel))
		if *updateWebGolden {
			if err := os.MkdirAll(filepath.Dir(gp), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(gp, out[p], 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := os.ReadFile(gp)
		if err != nil {
			t.Errorf("%s: %v (run go test -run TestWebGolden -update-web-golden)", rel, err)
			continue
		}
		if string(have) != string(out[p]) {
			t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", rel, out[p], have)
		}
	}
	var extra []string
	filepath.WalkDir(golden, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(golden, p)
			if !want[filepath.ToSlash(rel)] {
				extra = append(extra, rel)
			}
		}
		return nil
	})
	if len(extra) > 0 {
		t.Errorf("golden files no longer generated: %v", extra)
	}
	paths := make([]string, 0, len(out))
	for p := range out {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	wantPaths := []string{
		"web/src/i18n/locales/en/common.json",
		"web/src/i18n/locales/en/web.files.json",
		"web/src/i18n/locales/en/web.photos.json",
		"web/src/i18n/locales/es/common.json",
		"web/src/i18n/locales/es/web.files.json",
		"web/src/i18n/locales/es/web.photos.json",
		"web/src/i18n/messages.ts",
	}
	if !slices.Equal(paths, wantPaths) {
		t.Errorf("files = %v, want %v (shipping languages, web prefixes only)", paths, wantPaths)
	}
}

// webLocaleValues decodes a locale file.
func webLocaleValues(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%v:\n%s", err, data)
	}
	return m
}

func TestWebEscaping(t *testing.T) {
	out := emitWeb(t, loadChecked(t, webTestRepo(t)), false)
	files := string(out["web/src/i18n/locales/en/web.files.json"])
	for _, want := range []string{
		// Quotes, backslashes, the newline, and "<", ">", "&" HTML-escaped;
		// "'", "%", "$&" and non-ASCII as they are.
		`"web.files.escapes": "Quotes \" and ', a backslash in C:\\Users\\Ana, 100% \u0026 more \u003e less, $\u0026 and $1, %s%n, Español… 😀 {size}\nSecond line"`,
		// Tags kept (escaped as JSON), user arguments isolated, names in
		// camelCase.
		`"web.files.terms": "Read the \u003clink\u003eterms\u003c/link\u003e before \u003cb\u003e\u2068{name}\u2069\u003c/b\u003e signs"`,
		`"web.files.renamed": "\u2068{fileName}\u2069 is now {newName}"`,
	} {
		if !strings.Contains(files, want) {
			t.Errorf("web.files.json lacks\n%s\nin\n%s", want, files)
		}
	}
	// What the runtime gets once JSON.parse has run.
	var escapes, terms string
	m := webLocaleValues(t, out["web/src/i18n/locales/en/web.files.json"])
	json.Unmarshal(m["web.files.escapes"], &escapes)
	json.Unmarshal(m["web.files.terms"], &terms)
	if want := "Quotes \" and ', a backslash in C:\\Users\\Ana, 100% & more > less, $& and $1, %s%n, Español… 😀 {size}\nSecond line"; escapes != want {
		t.Errorf("decoded %q, want %q", escapes, want)
	}
	if want := "Read the <link>terms</link> before <b>\u2068{name}\u2069</b> signs"; terms != want {
		t.Errorf("decoded %q, want %q", terms, want)
	}
	// No raw isolation mark: a review of the file sees every one.
	for p, data := range out {
		if strings.ContainsAny(string(data), "\u2068\u2069\u2028\u2029") {
			t.Errorf("%s holds a raw invisible mark", p)
		}
	}
	// Texts never reach TypeScript source.
	ts := string(out[webMessagesFile])
	for _, text := range []string{"Quotes", "Cancel", "borró", "terms</link>", "Guardar", "Second line"} {
		if strings.Contains(ts, text) {
			t.Errorf("messages.ts contains the text %q", text)
		}
	}
}

func TestWebPluralShape(t *testing.T) {
	c := loadChecked(t, webTestRepo(t))
	out := emitWeb(t, c, true)
	for _, tc := range []struct{ code, one, other string }{
		{"en", "\u2068{name}\u2069 deleted a file", "\u2068{name}\u2069 deleted {files} files"},
		{"es", "\u2068{name}\u2069 borró un archivo", "\u2068{name}\u2069 borró {files} archivos"},
		// French has no translation: English, in the same shape.
		{"fr", "\u2068{name}\u2069 deleted a file", "\u2068{name}\u2069 deleted {files} files"},
		{"qps", "[\u2068{name}\u2069 délétéd á fílé ~~~~~]", "[\u2068{name}\u2069 délétéd {files} fíléš ~~~~~]"},
	} {
		m := webLocaleValues(t, out["web/src/i18n/locales/"+tc.code+"/web.files.json"])
		var p map[string]string
		if err := json.Unmarshal(m["web.files.deleted"], &p); err != nil {
			t.Fatalf("%s: a plural is an object: %v", tc.code, err)
		}
		want := map[string]string{"one": tc.one, "other": tc.other, "arg": "files"}
		if len(p) != len(want) || p["one"] != want["one"] || p["other"] != want["other"] || p["arg"] != want["arg"] {
			t.Errorf("%s: web.files.deleted = %q, want %q", tc.code, p, want)
		}
		var plain string
		if err := json.Unmarshal(m["web.files.renamed"], &plain); err != nil {
			t.Errorf("%s: a text without a count is a string: %v", tc.code, err)
		}
	}
	// The plural form is in the JSON as one object per key, its fields in a
	// fixed order.
	en := string(out["web/src/i18n/locales/en/web.files.json"])
	if want := "  \"web.files.deleted\": {\n    \"one\": \"\\u2068{name}\\u2069 deleted a file\",\n    \"other\": \"\\u2068{name}\\u2069 deleted {files} files\",\n    \"arg\": \"files\"\n  },\n"; !strings.Contains(en, want) {
		t.Errorf("plural layout:\n%s", en)
	}
}

func TestWebDraft(t *testing.T) {
	c := loadChecked(t, webTestRepo(t))
	out := emitWeb(t, c, true)
	for p, data := range out {
		if !strings.Contains(string(data), DraftMarker) {
			t.Errorf("%s lacks DraftMarker", p)
		}
	}
	for _, code := range []string{"en", "es", "fr", "pt", "qps"} {
		m := webLocaleValues(t, out["web/src/i18n/locales/"+code+"/common.json"])
		if _, ok := m[webDraftKey]; !ok || len(m) != 3 {
			t.Errorf("%s/common.json: the marker and the two keys, got %v", code, m)
		}
	}
	es := webLocaleValues(t, out["web/src/i18n/locales/es/web.files.json"])
	var renamed string
	json.Unmarshal(es["web.files.renamed"], &renamed)
	if renamed != "\u2068{fileName}\u2069 is now {newName}" {
		t.Errorf("a stale translation falls back to English, got %q", renamed)
	}
	ts := string(out[webMessagesFile])
	for _, want := range []string{
		`{ code: "pt", tag: "pt-PT", name: "Português", status: "draft" },`,
		`{ code: "qps", tag: "en-XA", name: "Pseudo", status: "draft" },`,
		"export type LanguageCode =\n  | \"en\"\n  | \"es\"\n  | \"fr\"\n  | \"pt\"\n  | \"qps\";\n",
	} {
		if !strings.Contains(ts, want) {
			t.Errorf("draft messages.ts lacks %q:\n%s", want, ts)
		}
	}
}

// TestWebThroughDriver runs the emitter the way make i18n does: generate,
// check, and remove what it no longer generates.
func TestWebThroughDriver(t *testing.T) {
	withEmitters(t, webEmitter{})
	root := webTestRepo(t)
	mustRun(t, root, 0, "8 generated files written") // 6 locale files, messages.ts, stored.json
	mustRun(t, root, 0, "generated files checked", "-check")
	write(t, root, "web/src/i18n/locales/de/common.json", "{}\n")
	mustRun(t, root, 1, "web/src/i18n/locales/de/common.json: error: no longer generated (by web)", "-check")
	mustRun(t, root, 0, "1 removed")
	mustRun(t, root, 0, "DRAFT output", "-draft")
	if !exists(root, "web/src/i18n/locales/qps/web.photos.json") {
		t.Error("-draft writes the pseudo-locale")
	}
	mustRun(t, root, 1, "holds draft output", "-check")
	mustRun(t, root, 0, "removed")
	mustRun(t, root, 0, "", "-check")
	// A file next to the generated ones that the emitter doesn't own stays.
	write(t, root, "web/src/i18n/t.ts", "export {}\n")
	mustRun(t, root, 0, "0 removed")
	if !exists(root, "web/src/i18n/t.ts") {
		t.Error("the runtime's own files are not the emitter's")
	}
}

// TestWebHostile runs hostile text past Check, straight into Emit: it comes
// out as inert JSON data, or as an error.
func TestWebHostile(t *testing.T) {
	langs := webTestLanguages
	entry := func(text, args, extra string) string {
		return `{"web.x.y": {"text": ` + text + `, "note": "n"` + args + extra + `}}`
	}
	for name, tc := range map[string]struct {
		files map[string]string
		err   string // "" = must succeed
	}{
		"script tag":   {map[string]string{"i18n/strings/en/web.x.json": entry(`"</script><img onerror=alert(1)>"`, "", "")}, "closes a tag that isn't open"},
		"script rich":  {map[string]string{"i18n/strings/en/web.x.json": entry(`"<script>alert(1)</script>"`, "", `, "rich": ["script"]`)}, ""},
		"quotes":       {map[string]string{"i18n/strings/en/web.x.json": entry(`"\"\"\""`, "", "")}, ""},
		"template":     {map[string]string{"i18n/strings/en/web.x.json": entry(`"{{.}}"`, "", "")}, "opens a placeholder that never closes"},
		"printf":       {map[string]string{"i18n/strings/en/web.x.json": entry(`"%s%n"`, "", "")}, ""},
		"replacement":  {map[string]string{"i18n/strings/en/web.x.json": entry(`"$& $' $1"`, "", "")}, ""},
		"separator":    {map[string]string{"i18n/strings/en/web.x.json": entry(`"a\u2028b\u2029c"`, "", "")}, ""},
		"undeclared":   {map[string]string{"i18n/strings/en/web.x.json": entry(`"{name}"`, "", "")}, "{name} is not a declared argument"},
		"untagged key": {map[string]string{"i18n/strings/en/web.x.json": entry(`"<b>x</b>"`, "", "")}, "<b> is not one of the key's tags"},
		"bytes":        {map[string]string{"i18n/strings/en/web.x.json": entry(`"{n}"`, `, "args": [["n", "bytes"]]`, "")}, `the web can't show type "bytes"`},
		"no count":     {map[string]string{"i18n/strings/en/web.x.json": entry(`{"one": "a", "other": "b"}`, "", "")}, "a plural text needs a count argument"},
		"key":          {map[string]string{"i18n/strings/en/web.x.json": `{"web.x.y\"; alert(1); //": {"text": "a", "note": "n"}}`}, "not a key"},
		"arg name":     {map[string]string{"i18n/strings/en/web.x.json": entry(`"{a}"`, `, "args": [["a", "text"], ["b: string; x", "text"]]`, "")}, "argument name"},
		"language name": {map[string]string{
			model.LanguagesFile:          strings.Replace(langs, `"Español"`, `"</script>\"'\u2028"`, 1),
			"i18n/strings/en/web.x.json": entry(`"x"`, "", ""),
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{model.LanguagesFile: langs}
			for p, s := range tc.files {
				files[p] = s
			}
			c, err := model.Load(writeRepo(t, files))
			if err != nil {
				t.Fatal(err)
			}
			if c.Entry("web.x.y") == nil && !strings.Contains(tc.files["i18n/strings/en/web.x.json"], "alert(1); //") {
				t.Fatal("the hostile entry wasn't loaded")
			}
			out, err := webEmitter{}.Emit(c, Options{Languages: c.OutputLanguages(false, nil)})
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for p, data := range out {
				if !strings.HasSuffix(p, ".json") {
					continue
				}
				if s := string(data); strings.ContainsAny(s, "<>&\u2028\u2029") {
					t.Errorf("%s holds a raw <, >, & or line separator:\n%s", p, s)
				}
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					t.Errorf("%s is not JSON: %v", p, err)
				}
				// The text comes back unchanged: data, not markup.
				if want := c.Entry("web.x.y").Text.Other; strings.Contains(p, "/web.x.json") && m["web.x.y"] != want {
					t.Errorf("%s: web.x.y = %q, want %q", p, m["web.x.y"], want)
				}
			}
			if name == "language name" {
				if want := `name: "\u003c/script\u003e\"'\u2028"`; !strings.Contains(string(out[webMessagesFile]), want) {
					t.Errorf("messages.ts lacks %s:\n%s", want, out[webMessagesFile])
				}
			}
		})
	}
}

// TestWebMessagesTypeCheck compiles the golden messages.ts with a file that
// uses it the way the runtime will, under the web app's strict options. It
// needs the web app's TypeScript (npm ci --prefix web) and skips without it.
func TestWebMessagesTypeCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("runs tsc")
	}
	tsc, err := filepath.Abs(filepath.Join("..", "..", "..", "web", "node_modules", ".bin", "tsc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tsc); err != nil {
		t.Skip("no web/node_modules: npm ci --prefix web")
	}
	out := emitWeb(t, loadChecked(t, webTestRepo(t)), true) // drafts: a longer language table
	dir := t.TempDir()
	write(t, dir, "messages.ts", string(out[webMessagesFile]))
	write(t, dir, "usage.ts", `import type { ArgsOf, Language, MessageKey, Messages, PluralMessage, RichKey, RichTags } from "./messages";
import { languages, sourceLanguage } from "./messages";

declare function t<K extends MessageKey>(key: K, ...args: ArgsOf<K>): string;
declare function trans<K extends RichKey>(key: K, parts: Record<RichTags[K], (s: string) => string>, ...args: ArgsOf<K>): string;

t("common.save");
t("web.files.deleted", { name: "Ana", files: 2 });
t("web.files.renamed", { fileName: "a", newName: "b" });
t("web.files.escapes", { size: 3 });
// @ts-expect-error not a key
t("web.files.nope");
// @ts-expect-error not a web key
t("app.title");
// @ts-expect-error arguments missing
t("web.files.deleted");
// @ts-expect-error a key without arguments takes none
t("common.save", {});
// @ts-expect-error a count is a number
t("web.files.deleted", { name: "Ana", files: "2" });
// @ts-expect-error an argument missing
t("web.files.deleted", { files: 2 });
// @ts-expect-error an argument too many
t("web.files.deleted", { name: "Ana", files: 2, more: 1 });
// @ts-expect-error the names are camelCase
t("web.files.renamed", { file_name: "a", new_name: "b" });
// @ts-expect-error a rich key isn't a plain lookup
t("web.files.terms", { name: "Ana" });
trans("web.files.terms", { link: (s) => s, b: (s) => s }, { name: "Ana" });
// @ts-expect-error every tag needs its renderer
trans("web.files.terms", { link: (s) => s }, { name: "Ana" });
// @ts-expect-error a plain key isn't rich
trans("common.save", {});

const m: Messages = { "common.save": "Save", "web.files.deleted": { one: "a", other: "b", arg: "files" } };
const p = m["web.files.deleted"] as PluralMessage;
const first: Language = languages[0];
const draft = languages.filter((l) => l.status === "draft").map((l) => l.tag);
export const used = [p.arg, first.code, sourceLanguage, draft];
`)
	cmd := exec.Command(tsc, "--noEmit", "--strict", "--erasableSyntaxOnly", "--verbatimModuleSyntax",
		"--noUnusedLocals", "--noUnusedParameters", "--moduleDetection", "force",
		"--target", "es2022", "--module", "esnext", "--moduleResolution", "bundler",
		"messages.ts", "usage.ts")
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc: %v\n%s", err, b)
	}
}
