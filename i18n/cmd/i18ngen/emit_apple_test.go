// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n/model"
)

// go test ./i18n/cmd/i18ngen -run TestAppleGolden -update-apple rewrites
// testdata/apple/golden from testdata/apple/repo.
var updateApple = flag.Bool("update-apple", false, "rewrite the apple emitter's golden files")

const (
	appleFixture = "testdata/apple/repo"
	appleGolden  = "testdata/apple/golden"
)

// appleFixtureCatalog loads the fixture catalog: English, Spanish, French
// and Portuguese shipping, with plurals, reordered and user arguments, a
// literal %, rich keys, a stale and a translate:false key and Info.plist
// texts for both apps.
func appleFixtureCatalog(t *testing.T) *model.Catalog {
	t.Helper()
	c, err := model.Load(appleFixture)
	if err != nil {
		t.Fatal(err)
	}
	if ds := c.Check(model.CheckOptions{}); model.Count(ds, model.Error) > 0 {
		t.Fatalf("fixture catalog has errors: %v", ds)
	}
	return c
}

func appleEmit(t *testing.T, c *model.Catalog, draft bool) map[string][]byte {
	t.Helper()
	out, err := appleEmitter{}.Emit(c, Options{Root: c.Root(), Draft: draft, Languages: c.OutputLanguages(draft, nil)})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAppleGolden(t *testing.T) {
	out := appleEmit(t, appleFixtureCatalog(t), false)
	if *updateApple {
		if err := os.RemoveAll(appleGolden); err != nil {
			t.Fatal(err)
		}
		for p, data := range out {
			write(t, appleGolden, p, string(data))
		}
	}
	golden := map[string]bool{}
	err := filepath.WalkDir(appleGolden, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(appleGolden, p)
		golden[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for p, data := range out {
		if !golden[p] {
			t.Errorf("%s: generated but not in %s (run with -update-apple)", p, appleGolden)
			continue
		}
		if want := read(t, appleGolden, p); want != string(data) {
			t.Errorf("%s differs from its golden file (run with -update-apple and review the diff):\n%s", p, data)
		}
	}
	for p := range golden {
		if _, ok := out[p]; !ok {
			t.Errorf("%s: golden file no longer generated", p)
		}
	}
	for _, want := range []string{
		"app/ios/OffTheCloud/OffTheCloud/i18n/Common.xcstrings",
		"app/ios/OffTheCloud/OffTheCloud/i18n/AppPhotos.xcstrings",
		"app/ios/OffTheCloud/OffTheCloud/i18n/S+AppSettings.swift",
		"app/ios/OffTheCloud/OffTheCloud/i18n/Ios.xcstrings",
		"app/ios/OffTheCloud/OffTheCloud/i18n/InfoPlist.xcstrings",
		"app/ios/OffTheCloud/OffTheCloud/i18n/Languages.swift",
		"app/macos/OffTheCloud/OffTheCloud/i18n/Common.xcstrings",
		"app/macos/OffTheCloud/OffTheCloud/i18n/Desk.xcstrings",
		"app/macos/OffTheCloud/OffTheCloud/i18n/InfoPlist.xcstrings",
	} {
		if _, ok := out[want]; !ok {
			t.Errorf("%s was not generated", want)
		}
	}
	for _, unwanted := range []string{
		"app/macos/OffTheCloud/OffTheCloud/i18n/AppPhotos.xcstrings", // app.* is the phones'
		"app/ios/OffTheCloud/OffTheCloud/i18n/Desk.xcstrings",        // desk.* the computers'
		"app/macos/OffTheCloud/OffTheCloud/i18n/Macos.xcstrings",     // macos.json holds only plist keys
	} {
		if _, ok := out[unwanted]; ok {
			t.Errorf("%s should not be generated", unwanted)
		}
	}
}

// TestAppleCatalogShape checks the decoded catalogs: states, the
// substitutions form, escaping, and Xcode's layout.
func TestAppleCatalogShape(t *testing.T) {
	out := appleEmit(t, appleFixtureCatalog(t), false)
	dir := "app/ios/OffTheCloud/OffTheCloud/i18n/"
	unit := func(file, key, lang string, path ...string) (state, value string) {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal(out[dir+file], &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		var v any = doc["strings"].(map[string]any)[key].(map[string]any)["localizations"].(map[string]any)[lang]
		for _, p := range append(path, "stringUnit") {
			v = v.(map[string]any)[p]
		}
		u := v.(map[string]any)
		return u["state"].(string), u["value"].(string)
	}
	plural := func(file, key, lang, form string) (string, string) {
		return unit(file, key, lang, "substitutions", "count", "variations", "plural", form)
	}
	for _, tc := range []struct{ got, want [2]string }{
		{pair(unit("Common.xcstrings", "common.cancel", "es")), [2]string{"translated", "Cancelar"}},
		{pair(unit("Common.xcstrings", "common.cancel", "fr")), [2]string{"new", "Cancel"}},
		{pair(unit("AppPhotos.xcstrings", "app.photos.stale", "es")), [2]string{"needs_review", "Pick the photos to keep"}},
		{pair(unit("AppPhotos.xcstrings", "app.photos.count", "en")), [2]string{"translated", "%#@count@"}},
		{pair(plural("AppPhotos.xcstrings", "app.photos.count", "pt-PT", "other")), [2]string{"translated", "%arg fotografias"}},
		{pair(plural("AppPhotos.xcstrings", "app.photos.deleted_by", "en", "one")), [2]string{"translated", "%1$@ deleted a photo"}},
		{pair(plural("AppPhotos.xcstrings", "app.photos.deleted_from", "es", "other")), [2]string{"translated", "%1$@ borró %arg fotos de %3$@"}},
		{pair(unit("AppPhotos.xcstrings", "app.photos.shared_by", "es")), [2]string{"translated", "Compartido por %2$@: %1$@"}},
		{pair(unit("AppSettings.xcstrings", "app.settings.storage_used", "en")), [2]string{"translated", "%1$lld%% used of %2$lld GB"}},
		{pair(unit("AppSettings.xcstrings", "app.settings.brand", "es")), [2]string{"translated", "Off The Cloud"}},
		{pair(unit("AppSettings.xcstrings", "app.settings.quotes", "en")), [2]string{"translated", "Type \"delete\" to confirm.\nThis can't be undone: C:\\ & <b>bold</b> 100%%"}},
		{pair(unit("InfoPlist.xcstrings", "NSCameraUsageDescription", "es")), [2]string{"translated", "Para hacer una foto o un vídeo y publicarlo (100 % opcional)"}},
		{pair(unit("InfoPlist.xcstrings", "NSCameraUsageDescription", "en")), [2]string{"translated", "Used to take a photo or video to post to your feed (100% optional)"}},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}

	var doc map[string]any
	json.Unmarshal(out[dir+"AppPhotos.xcstrings"], &doc)
	sub := doc["strings"].(map[string]any)["app.photos.deleted_from"].(map[string]any)["localizations"].(map[string]any)["en"].(map[string]any)["substitutions"].(map[string]any)["count"].(map[string]any)
	if sub["argNum"] != 2.0 || sub["formatSpecifier"] != "lld" {
		t.Errorf("substitution = %v, want argNum 2, lld", sub)
	}

	for p, data := range out {
		if strings.HasSuffix(p, ".xcstrings") {
			if !bytes.HasPrefix(data, []byte("{\n  \"sourceLanguage\" : \"en\",\n  \"strings\" : {\n")) ||
				!bytes.HasSuffix(data, []byte("\n  },\n  \"version\" : \"1.0\"\n}")) {
				t.Errorf("%s is not in Xcode's layout:\n%s", p, data)
			}
			// What Xcode reads back, written again, is the same bytes.
			var v any
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			var b strings.Builder
			writeXC(&b, toXC(v), "")
			if b.String() != string(data) {
				t.Errorf("%s does not round-trip", p)
			}
			if !bytes.Contains(data, []byte(`"extractionState" : "manual"`)) || bytes.Contains(data, []byte(`"extracted`)) {
				t.Errorf("%s: every entry must be manual", p)
			}
			if bytes.Contains(data, []byte("Localizable")) {
				t.Errorf("%s mentions Localizable", p)
			}
		}
	}
}

func pair(a, b string) [2]string { return [2]string{a, b} }

// toXC converts decoded JSON back to the writer's types.
func toXC(v any) any {
	switch v := v.(type) {
	case map[string]any:
		o := xcObject{}
		for k, x := range v {
			o[k] = toXC(x)
		}
		return o
	case float64:
		return int(v)
	}
	return v
}

func TestAppleAccessors(t *testing.T) {
	out := appleEmit(t, appleFixtureCatalog(t), false)
	dir := "app/ios/OffTheCloud/OffTheCloud/i18n/"
	for file, wants := range map[string][]string{
		"S+Common.swift": {
			"// Code generated by i18ngen from i18n/strings/en/common.json. DO NOT EDIT.",
			"extension S {",
			"    /// Cancel\n    ///\n    /// Button that closes a dialog.\n    static var commonCancel: String { L(\"common.cancel\", table: \"Common\") }",
		},
		"S+AppPhotos.swift": {
			"    /// one: {name} deleted a photo\n    /// other: {name} deleted {count} photos\n",
			"    static func appPhotosDeletedBy(name: String, count: Int) -> String {\n        L(\"app.photos.deleted_by\", table: \"AppPhotos\", L10n.isolate(name), count)\n    }",
			"    static func appPhotosDeletedFrom(name: String, count: Int, place: String) -> String {\n        L(\"app.photos.deleted_from\", table: \"AppPhotos\", L10n.isolate(name), count, place)\n    }",
		},
		"S+AppSettings.swift": {
			"    static func appSettingsStorageUsed(percent: Int, total: Int) -> String {",
			"    static func appSettingsTerms(name: String, terms: AttributeContainer) -> AttributedString {\n        LRich(\"app.settings.terms\", table: \"AppSettings\", tags: [\"terms\": terms], [.text(L10n.isolate(name))])\n    }",
			"    static func appSettingsQuotes(b: AttributeContainer) -> AttributedString {\n        LRich(\"app.settings.quotes\", table: \"AppSettings\", tags: [\"b\": b], [])\n    }",
			"    /// Type \"delete\" to confirm.\n    /// This can't be undone: C:\\ & <b>bold</b> 100%\n",
		},
		"S+Ios.swift": {"static var iosTabPhotos: String"},
		"Languages.swift": {
			"        L10nLanguage(code: \"en\", tag: \"en\", apple: \"en\", name: \"English\", pseudo: false),\n" +
				"        L10nLanguage(code: \"es\", tag: \"es\", apple: \"es\", name: \"Español\", pseudo: false),\n" +
				"        L10nLanguage(code: \"fr\", tag: \"fr\", apple: \"fr\", name: \"Français\", pseudo: false),\n" +
				"        L10nLanguage(code: \"pt\", tag: \"pt-PT\", apple: \"pt-PT\", name: \"Português\", pseudo: false),\n    ]",
		},
	} {
		got := string(out[dir+file])
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", file, w, got)
			}
		}
	}
	if s := string(out[dir+"S+Ios.swift"]); strings.Contains(s, "plist") {
		t.Errorf("Info.plist texts get no accessor:\n%s", s)
	}
}

func TestAppleDraft(t *testing.T) {
	c := appleFixtureCatalog(t)
	out := appleEmit(t, c, true)
	for p, data := range out {
		if !bytes.Contains(data, []byte(DraftMarker)) {
			t.Errorf("%s: draft output without the marker", p)
		}
	}
	langs := string(out["app/macos/OffTheCloud/OffTheCloud/i18n/Languages.swift"])
	for _, w := range []string{`code: "de"`, `code: "nl"`, `code: "qps", tag: "en-XA", apple: "en-XA", name: "Pseudo", pseudo: true`} {
		if !strings.Contains(langs, w) {
			t.Errorf("draft Languages.swift lacks %s:\n%s", w, langs)
		}
	}
	common := string(out["app/macos/OffTheCloud/OffTheCloud/i18n/Common.xcstrings"])
	if !strings.Contains(common, `"en-XA" : {`) || !strings.Contains(common, `"value" : "[Çáñçél ~~]"`) {
		t.Errorf("draft catalog lacks the pseudo-locale:\n%s", common)
	}
	// The plain run removes what the draft run added.
	plain := appleEmit(t, c, false)
	if len(plain) != len(out) {
		t.Errorf("draft and plain runs write different files: %d vs %d", len(out), len(plain))
	}
}

func TestAppleFormat(t *testing.T) {
	e := &model.Entry{Key: "app.x", Args: []model.Arg{{Name: "name", Type: model.ArgUser}, {Name: "count", Type: model.ArgCount}, {Name: "size", Type: model.ArgInt}, {Name: "file_name", Type: model.ArgText}}, Rich: []string{"b"}}
	for _, tc := range []struct {
		form   string
		plural bool
		want   string
		err    string
	}{
		{"{name} has {count} of {size}", true, "%1$@ has %arg of %3$lld", ""},
		{"{file_name} {name}", false, "%4$@ %1$@", ""},
		{"100% of {size}%", false, "100%% of %3$lld%%", ""},
		{"%s%n %@ %1$@ $& {{.}}", false, "", "never closes"},
		{"%s%n %@ %1$@ $&", false, "%%s%%n %%@ %%1$@ $&", ""},
		{`"""`, false, `"""`, ""},
		{"<b>{name}</b>", false, "<b>%1$@</b>", ""},
		{"<i>x</i>", false, "", "not one of the key's tags"},
		{"</script><img onerror>", false, "", "isn't open"},
		{"{count}", false, "", "isn't plural"},
		{"{nope}", false, "", "not a declared argument"},
	} {
		got, err := appleFormat(e, tc.form, tc.plural)
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: error %v, want %q", tc.form, err, tc.err)
		case tc.err == "" && (err != nil || got != tc.want):
			t.Errorf("%q = %q, %v; want %q", tc.form, got, err, tc.want)
		}
	}
}

func TestAppleXCWriter(t *testing.T) {
	var b strings.Builder
	writeXC(&b, xcObject{
		"b":     "say \"hi\"\n\t\\ / Ñandú 😀 <&> \u2028 \x01",
		"a":     xcObject{},
		"n":     2,
		"inner": xcObject{"z": "1", "y": xcObject{"x": "2"}},
	}, "")
	want := "{\n" +
		"  \"a\" : {\n\n  },\n" +
		"  \"b\" : \"say \\\"hi\\\"\\n\\t\\\\ / Ñandú 😀 <&> \u2028 \\u0001\",\n" +
		"  \"inner\" : {\n    \"y\" : {\n      \"x\" : \"2\"\n    },\n    \"z\" : \"1\"\n  },\n" +
		"  \"n\" : 2\n" +
		"}"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}

// TestAppleXcodeOrder holds the orders Xcode 27 wrote when
// -exportLocalizations rewrote a catalog.
func TestAppleXcodeOrder(t *testing.T) {
	for _, want := range [][]string{
		{"ios.a_b", "ios.a-b", "ios.a.b", "ios.a~b", "ios.a9", "ios.aa", "ios.ab", "ios.Ab", "ios.ab_c", "ios.ab.d", "ios.ab1", "ios.aZ", "ios.b"},
		{"ios.a_9", "ios.a_10", "ios.a2", "ios.a02", "ios.a2b", "ios.a9", "ios.a10", "ios.ab_c", "ios.ab.d", "ios.b1", "ios.b1_c", "ios.b1.c", "ios.b10",
			"ios.step_2", "ios.step.2", "ios.step2", "ios.step10", "ios.x_y", "ios.x.1", "ios.x.y", "ios.x1", "ios.x1y", "ios.x2y", "ios.x10y", "ios.xy"},
		{"en", "en-XA", "pt", "pt-PT"},
		{"argNum", "formatSpecifier", "variations"},
		{"comment", "extractionState", "localizations"},
		{"sourceLanguage", "strings", "version"},
		{"stringUnit", "substitutions"},
		{"CFBundleName", "NSBluetoothAlwaysUsageDescription", "NSCameraUsageDescription", "NSLocalNetworkUsageDescription", "NSPhotoLibraryAddUsageDescription", "NSPhotoLibraryUsageDescription"},
	} {
		got := append([]string(nil), want...)
		for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
			got[i], got[j] = got[j], got[i]
		}
		sort.Slice(got, func(i, j int) bool { return xcodeLess(got[i], got[j]) })
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("sorted:\n%v\nwant:\n%v", got, want)
		}
	}
}

// appleTestLanguages routes the Apple roots for the small catalogs below.
const appleTestLanguages = `{
  "languages": [
    {"code": "en", "tag": "en", "apple": "en", "android": "", "name": "English", "status": "shipping"},
    {"code": "es", "tag": "es", "apple": "es", "android": "es", "name": "Español", "status": "draft"}
  ],
  "routes": {"common": ["ios", "macos"], "app": ["ios"], "ios": ["ios"], "macos": ["macos"], "desk": ["macos"], "appphotos": ["ios"], "localizable": ["macos"]}
}
`

// appleTry loads a catalog from path -> content, without checking it, and
// runs the emitter on it.
func appleTry(t *testing.T, files map[string]string) (map[string][]byte, error) {
	t.Helper()
	files[model.LanguagesFile] = appleTestLanguages
	c, err := model.Load(writeRepo(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return appleEmitter{}.Emit(c, Options{Languages: c.OutputLanguages(false, nil)})
}

func appleEntry(key, text, note, extra string) string {
	k, _ := json.Marshal(key)
	tx, _ := json.Marshal(text)
	n, _ := json.Marshal(note)
	return `{` + string(k) + `: {"text": ` + string(tx) + `, "note": ` + string(n) + extra + `}}`
}

// TestAppleHostile runs the hostile catalog of docs/i18n.md through the
// emitter: every text comes out inert in the catalogs, or as an error, and
// nothing from a text or a note becomes Swift code.
func TestAppleHostile(t *testing.T) {
	for _, s := range []string{"</script><img onerror>", `"""`, "{{.}}", "%s%n", "$&", "a\\b \u202Ex", "x\n    }\n    static var pwned: Int { 1 }\n    //"} {
		for _, note := range []string{"plain", s} {
			out, err := appleTry(t, map[string]string{"i18n/strings/en/app.json": appleEntry("app.x", s, note, "")})
			if err != nil {
				continue // refused: fine
			}
			var doc map[string]any
			cat := out["app/ios/OffTheCloud/OffTheCloud/i18n/App.xcstrings"]
			if err := json.Unmarshal(cat, &doc); err != nil {
				t.Fatalf("%q: catalog isn't JSON: %v\n%s", s, err, cat)
			}
			v := doc["strings"].(map[string]any)["app.x"].(map[string]any)["localizations"].(map[string]any)["en"].(map[string]any)["stringUnit"].(map[string]any)["value"]
			if want := strings.ReplaceAll(s, "%", "%%"); v != want {
				t.Errorf("%q came out as %q", s, v)
			}
			swift := string(out["app/ios/OffTheCloud/OffTheCloud/i18n/S+App.swift"])
			for _, line := range strings.Split(swift, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.Contains(line, "pwned") && !strings.HasPrefix(trimmed, "///") {
					t.Errorf("text or note %q became code:\n%s", s, swift)
				}
				if strings.ContainsRune(line, '\u202E') {
					t.Errorf("a bidi override reached the Swift source:\n%s", swift)
				}
			}
		}
	}
}

func TestAppleErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		err   string
	}{
		"unknown plist key": {map[string]string{"i18n/strings/en/ios.json": appleEntry("ios.plist.nsfoo", "x", "n", "")}, "not a localizable Info.plist key"},
		"plist key with an argument": {map[string]string{"i18n/strings/en/ios.json": appleEntry("ios.plist.nscamerausagedescription", "{n}", "n", `, "args": [["n", "text"]]`)},
			"can't have arguments"},
		"tag named like an argument": {map[string]string{"i18n/strings/en/app.json": appleEntry("app.x", "<name>{name}</name>", "n", `, "args": [["name", "text"]], "rich": ["name"]`)},
			"has the name of an argument"},
		"tag that is a keyword": {map[string]string{"i18n/strings/en/app.json": appleEntry("app.x", "<in>x</in>", "n", `, "rich": ["in"]`)}, "Swift keyword"},
		"tables differing only by case": {map[string]string{
			"i18n/strings/en/appphotos.json":  appleEntry("appphotos.x", "x", "n", ""),
			"i18n/strings/en/app.photos.json": appleEntry("app.photos.y", "y", "n", ""),
		}, "case-insensitive file system"},
		"a Localizable table":       {map[string]string{"i18n/strings/en/localizable.json": appleEntry("localizable.x", "x", "n", "")}, "Xcode reserves"},
		"a stored-data argument":    {map[string]string{"i18n/strings/en/app.json": appleEntry("app.x", "{size}", "n", `, "args": [["size", "bytes"]]`)}, "can't show a bytes argument"},
		"an injected argument name": {map[string]string{"i18n/strings/en/app.json": appleEntry("app.x", "x", "n", `, "args": [["a: Int) {}; func f(b", "text"]]`)}, "argument"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := appleTry(t, tc.files); err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("error %v, want %q", err, tc.err)
			}
		})
	}
}

// TestApplePlistKeepsXcodeEntries: the entries Xcode's sync adds to
// InfoPlist.xcstrings survive make i18n, so a synced file stays up to date.
func TestApplePlistKeepsXcodeEntries(t *testing.T) {
	const rel = "app/ios/OffTheCloud/OffTheCloud/i18n/InfoPlist.xcstrings"
	files := map[string]string{
		model.LanguagesFile:        appleTestLanguages,
		"i18n/strings/en/ios.json": appleEntry("ios.plist.nscamerausagedescription", "Camera, 100%", "n", ""),
		rel: `{"sourceLanguage": "en", "version": "1.0", "strings": {
  "CFBundleName": {"comment": "Bundle name", "extractionState": "extracted_with_value", "shouldTranslate": false,
    "localizations": {"en": {"stringUnit": {"state": "new", "value": "OffTheCloud"}}}},
  "NSCameraUsageDescription": {"extractionState": "extracted_with_value",
    "localizations": {"en": {"stringUnit": {"state": "new", "value": "old"}}}},
  "NSMicrophoneUsageDescription": {"extractionState": "manual",
    "localizations": {"en": {"stringUnit": {"state": "translated", "value": "gone"}}}}}}`,
	}
	root := writeRepo(t, files)
	emit := func() string {
		t.Helper()
		c, err := model.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		out, err := appleEmitter{}.Emit(c, Options{Root: root, Languages: c.OutputLanguages(false, nil)})
		if err != nil {
			t.Fatal(err)
		}
		return string(out[rel])
	}
	got := emit()
	for _, want := range []string{
		"    \"CFBundleName\" : {\n      \"comment\" : \"Bundle name\",\n      \"extractionState\" : \"extracted_with_value\",",
		"\"shouldTranslate\" : false",
		"\"value\" : \"Camera, 100%\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "gone") || strings.Contains(got, `"old"`) {
		t.Errorf("a manual entry the catalog no longer has, or an extracted one it now defines, was kept:\n%s", got)
	}
	write(t, root, rel, got)
	if again := emit(); again != got {
		t.Errorf("not stable:\n%s", again)
	}

	write(t, root, rel, "<<<<<<< HEAD\n")
	c, _ := model.Load(root)
	if _, err := (appleEmitter{}).Emit(c, Options{Root: root, Languages: c.OutputLanguages(false, nil)}); err == nil || !strings.Contains(err.Error(), "delete it and run make i18n") {
		t.Errorf("a broken InfoPlist.xcstrings: error %v", err)
	}
}

func TestAppleSwiftQuote(t *testing.T) {
	for in, want := range map[string]string{
		"Español":          `"Español"`,
		`a"b\(c)`:          `"a\"b\\(c)"`,
		"x\u202Ey\u00a0\n": `"x\u{202E}y\u{A0}\n"`,
		"\u2068name\u2069": `"\u{2068}name\u{2069}"`,
	} {
		if got := swiftQuote(in); got != want {
			t.Errorf("swiftQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
