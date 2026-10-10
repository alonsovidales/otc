// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n/model"
)

var updateAndroidGolden = flag.Bool("update-android-golden", false, "rewrite testdata/android/res and java from the android emitter's output")

const (
	// androidFixture is a catalog with every escaping case, four languages
	// (pt a draft) and keys routed elsewhere. Its outputs are the goldens
	// in testdata/android/res and testdata/android/java.
	androidFixture   = "testdata/android/catalog"
	androidGoldenDir = "testdata/android"
)

// Characters the tests spell out, so that this file holds none of them.
var (
	androidTestBSU = "\\" + "u" // the start of an aapt2 \uXXXX escape
	androidTestFSI = string(rune(0x2068))
	androidTestPDI = string(rune(0x2069))
)

// androidLoadFixture loads the fixture catalog and checks it has no errors.
func androidLoadFixture(t *testing.T) *model.Catalog {
	t.Helper()
	c, err := model.Load(androidFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range c.Check(model.CheckOptions{}) {
		if d.Severity == model.Error {
			t.Fatalf("fixture catalog: %s", d)
		}
	}
	return c
}

// androidAllLanguages is every language of a catalog and the pseudo-locale.
func androidAllLanguages(c *model.Catalog) []model.Language {
	return append(c.AllLanguages(), model.PseudoLanguage)
}

func androidEmit(t *testing.T, c *model.Catalog, opts Options) map[string][]byte {
	t.Helper()
	out, err := androidEmitter{}.Emit(c, opts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAndroidGolden compares every file with testdata/android (go test -run
// TestAndroidGolden -update-android-golden rewrites them).
func TestAndroidGolden(t *testing.T) {
	c := androidLoadFixture(t)
	out := androidEmit(t, c, Options{Root: androidFixture, Languages: androidAllLanguages(c)})
	want := map[string][]byte{}
	for p, data := range out {
		rel, ok := strings.CutPrefix(p, androidMain+"/")
		if !ok {
			t.Fatalf("%s is outside %s", p, androidMain)
		}
		want[filepath.Join(androidGoldenDir, filepath.FromSlash(rel))] = data
	}
	if *updateAndroidGolden {
		for _, d := range []string{"res", "java"} {
			if err := os.RemoveAll(filepath.Join(androidGoldenDir, d)); err != nil {
				t.Fatal(err)
			}
		}
		for p, data := range want {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	have := map[string]bool{}
	for _, d := range []string{"res", "java"} {
		filepath.WalkDir(filepath.Join(androidGoldenDir, d), func(p string, de fs.DirEntry, err error) error {
			if err == nil && !de.IsDir() {
				have[p] = true
			}
			return nil
		})
	}
	for p, data := range want {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v (run go test -run TestAndroidGolden -update-android-golden)", p, err)
			continue
		}
		if string(got) != string(data) {
			t.Errorf("%s differs from the emitter's output (run go test -run TestAndroidGolden -update-android-golden):\n%s", p, data)
		}
	}
	for p := range have {
		if _, ok := want[p]; !ok {
			t.Errorf("%s is not generated any more", p)
		}
	}
	// en, es, fr, de, pt and the pseudo-locale with 3 prefixes each, the 3
	// S_<Prefix>.kt, Languages.kt and locales_config.xml.
	if len(want) != 6*3+3+2 {
		t.Errorf("%d files generated, want %d", len(want), 6*3+3+2)
	}
}

// androidTestEntry builds an entry for androidValue.
func androidTestEntry(rich bool, args ...string) *model.Entry {
	e := &model.Entry{Key: "android.t", Root: "android", Prefix: "android", Translate: true}
	for i := 0; i+1 < len(args); i += 2 {
		e.Args = append(e.Args, model.Arg{Name: args[i], Type: model.ArgType(args[i+1])})
	}
	if rich {
		e.Rich = []string{"b"}
	}
	return e
}

func TestAndroidValues(t *testing.T) {
	u := androidTestBSU
	zwj := string(rune(0x200d))
	for _, tc := range []struct {
		form string
		e    *model.Entry
		want string // "" with err
		err  string
	}{
		{"Fish & chips > fries", androidTestEntry(false), "Fish &amp; chips &gt; fries", ""},
		{`It's "x" \ y`, androidTestEntry(false), `It\'s \"x\" \\ y`, ""},
		{`"""`, androidTestEntry(false), `\"\"\"`, ""},
		{"@string/app_name", androidTestEntry(false), `\@string/app_name`, ""},
		{"a @b", androidTestEntry(false), "a @b", ""},
		{"?attr/x", androidTestEntry(false), `\?attr/x`, ""},
		{"x?", androidTestEntry(false), "x?", ""},
		{"100% %s%n $&", androidTestEntry(false), "100% %s%n $&amp;", ""},
		{"{n} 100% %s%n", androidTestEntry(false, "n", "text"), "%1$s 100%% %%s%%n", ""},
		{"a\nb\tc", androidTestEntry(false), `a\nb\tc`, ""},
		{" a  b ", androidTestEntry(false), u + "0020a" + u + "0020" + u + "0020b" + u + "0020", ""},
		{"a \nb", androidTestEntry(false), "a" + u + "0020\\nb", ""},
		{"a b c", androidTestEntry(false), "a b c", ""},
		{"{name} x", androidTestEntry(false, "name", "user"), u + "2068%1$s" + u + "2069 x", ""},
		{"{b} {a}", androidTestEntry(false, "a", "text", "b", "int"), "%2$s %1$s", ""},
		{"<b>{n}</b>!", androidTestEntry(true, "n", "count"), "&lt;b&gt;%1$s&lt;/b&gt;!", ""},
		{"x" + zwj + "y " + string(rune(0xa0)) + "z", androidTestEntry(false), "x" + u + "200Dy " + string(rune(0xa0)) + "z", ""},
		{"Qué 😀", androidTestEntry(false), "Qué 😀", ""},
		{"x" + string(rune(0xe000)), androidTestEntry(false), "x" + string(rune(0xe000)), ""},
		{"x" + string(rune(0xe000)), androidTestEntry(true), "", "private-use"},
		{"x\x01", androidTestEntry(false), "", "U+0001"},
		{"x" + string(rune(0xfffe)), androidTestEntry(false), "", "U+FFFE"},
		{"x\xff", androidTestEntry(false), "", "invalid UTF-8"},
		{"{zzz}", androidTestEntry(false), "", "undeclared placeholder"},
		{"</script><img onerror>", androidTestEntry(false), "", "isn't open"},
		{"{{.}}", androidTestEntry(false), "", "never closes"},
	} {
		got, err := androidValue(tc.e, tc.form)
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("androidValue(%q): err %v, want %q", tc.form, err, tc.err)
		case tc.err == "" && err != nil:
			t.Errorf("androidValue(%q): %v", tc.form, err)
		case got != tc.want:
			t.Errorf("androidValue(%q) = %q, want %q", tc.form, got, tc.want)
		}
	}
}

// androidElement returns a <string> or <plurals> element of a generated file.
func androidElement(t *testing.T, out map[string][]byte, dir, prefix, name string) string {
	t.Helper()
	data, ok := out[path.Join(androidRes, dir, androidStringsPre+prefix+".xml")]
	if !ok {
		t.Fatalf("no %s/%s file", dir, prefix)
	}
	s := string(data)
	i := strings.Index(s, `name="`+name+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(s[:i], "<")
	closing := "</string>"
	if strings.HasPrefix(s[start:], "<plurals") {
		closing = "</plurals>"
	}
	return s[start : i+strings.Index(s[i:], closing)+len(closing)]
}

func TestAndroidPluralsAndRoutes(t *testing.T) {
	c := androidLoadFixture(t)
	out := androidEmit(t, c, Options{Languages: androidAllLanguages(c)})
	el := func(dir, prefix, name string) string { return androidElement(t, out, dir, prefix, name) }

	// many repeats other for es, fr, it and pt only.
	if p := el("values-es", "app_photos", "app_photos_count"); !strings.Contains(p, `<item quantity="many">%1$s photos</item>`) {
		t.Errorf("es plural without many:\n%s", p)
	}
	for _, dir := range []string{"values", "values-de", "values-" + androidPseudoQualifier} {
		if p := el(dir, "app_photos", "app_photos_count"); strings.Contains(p, "many") {
			t.Errorf("%s plural with many:\n%s", dir, p)
		}
	}
	// French: 0 is "one", so the English one without its count (a missing
	// translation) gives way to other.
	if p := el("values-fr", "app_photos", "app_photos_deleted_by"); !strings.Contains(p, `<item quantity="one">`+androidTestBSU+`2068%1$s`+androidTestBSU+`2069 deleted %2$s photos</item>`) {
		t.Errorf("fr fallback one without the count:\n%s", p)
	}
	// pt-PT's one is exactly 1: kept, with lint told (it reads values-pt as
	// Brazilian); with the count in it, nothing to tell.
	if p := el("values-pt", "app_photos", "app_photos_deleted_by"); !strings.Contains(p, `tools:ignore="ImpliedQuantity"`) || !strings.Contains(p, "apagou uma fotografia") {
		t.Errorf("pt one:\n%s", p)
	}
	if p := el("values-pt", "app_photos", "app_photos_count"); strings.Contains(p, "tools:ignore") {
		t.Errorf("pt one with its count needs no ignore:\n%s", p)
	}
	if p := el("values", "app_photos", "app_photos_deleted_by"); strings.Contains(p, "tools:ignore") || !strings.Contains(p, "deleted a photo") {
		t.Errorf("English one:\n%s", p)
	}
	// Stale and missing translations are English; current ones are used.
	if p := el("values-es", "common", "common_save"); !strings.Contains(p, ">Save<") {
		t.Errorf("stale es translation used: %s", p)
	}
	if p := el("values-es", "common", "common_cancel"); !strings.Contains(p, ">Cancelar<") {
		t.Errorf("es translation not used: %s", p)
	}
	// translate: false lives in the default resources only.
	if p := el("values", "android", "android_esc_fixed"); !strings.Contains(p, `translatable="false"`) {
		t.Errorf("untranslatable key: %s", p)
	}
	for _, dir := range []string{"values-es", "values-" + androidPseudoQualifier} {
		if p := el(dir, "android", "android_esc_fixed"); p != "" {
			t.Errorf("%s holds an untranslatable key: %s", dir, p)
		}
	}
	// web.* isn't routed to Android.
	for p, data := range out {
		if strings.Contains(p, "web") || strings.Contains(string(data), "web_only") || strings.Contains(string(data), "webOnly") {
			t.Errorf("%s: web key in Android output", p)
		}
	}
	// formatted="false" only on keys without arguments that have a %.
	if p := el("values", "android", "android_esc_percent"); !strings.Contains(p, `formatted="false">100% sure<`) {
		t.Errorf("percent without arguments: %s", p)
	}
	if p := el("values", "android", "android_esc_percent_args"); strings.Contains(p, "formatted") || !strings.Contains(p, "100%% sure, %%s%%n") {
		t.Errorf("percent with arguments: %s", p)
	}
}

func TestAndroidKotlin(t *testing.T) {
	c := androidLoadFixture(t)
	out := androidEmit(t, c, Options{Languages: androidAllLanguages(c)})
	kt := string(out[androidKotlinDir+"/S_AppPhotos.kt"])
	for _, want := range []string{
		"fun S.appPhotosDeletedBy(name: String, count: Int): UiText =\n    UiText.Plural(R.plurals.app_photos_deleted_by, count, listOf(name, count))\n",
		"fun S.appPhotosPrivacy(fileName: String): RichText =\n    RichText.Res(R.string.app_photos_privacy, listOf(fileName))\n",
		"fun S.appPhotosRemoved(count: Int): RichText =\n    RichText.Plural(R.plurals.app_photos_removed, count, listOf(count))\n",
		"fun S.appPhotosSlots(used: Int, total: Int): UiText =\n    UiText.Res(R.string.app_photos_slots, listOf(used, total))\n",
	} {
		if !strings.Contains(kt, want) {
			t.Errorf("S_AppPhotos.kt lacks:\n%s\n---\n%s", want, kt)
		}
	}
	langs := string(out[androidLanguages])
	for _, want := range []string{
		`Language(code = "pt", tag = "pt-PT", name = "Português"),`,
		`Language(code = "qps", tag = "` + androidPseudoTag + `", name = "Pseudo"),`,
	} {
		if !strings.Contains(langs, want) {
			t.Errorf("Languages.kt lacks %s:\n%s", want, langs)
		}
	}
	if lc := string(out[androidLocales]); !strings.Contains(lc, `<locale android:name="pt-PT" />`) || !strings.Contains(lc, `<locale android:name="`+androidPseudoTag+`" />`) {
		t.Errorf("locales_config.xml:\n%s", lc)
	}
}

// androidFixtureFiles reads the fixture catalog as path -> content.
func androidFixtureFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(androidFixture, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(androidFixture, p)
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

const androidHandWritten = `<resources>
    <string name="app_name">Off The Cloud</string>
</resources>
`

func TestAndroidThroughDriver(t *testing.T) {
	withEmitters(t, androidEmitter{})
	files := androidFixtureFiles(t)
	files[androidRes+"/values/strings.xml"] = androidHandWritten
	files[androidKotlinDir+"/UiText.kt"] = "// hand-written\n"
	root := writeRepo(t, files)

	mustRun(t, root, 0, "DRAFT output", "-draft")
	var drafts []string
	filepath.WalkDir(filepath.Join(root, "app"), func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		data, _ := os.ReadFile(p)
		if strings.Contains(string(data), DraftMarker) {
			drafts = append(drafts, rel)
		}
		return nil
	})
	sort.Strings(drafts)
	for _, want := range []string{
		androidRes + "/values-pt/strings_common.xml",
		androidRes + "/values-" + androidPseudoQualifier + "/strings_android.xml",
		androidLocales,
		androidLanguages,
		androidKotlinDir + "/S_Common.kt",
	} {
		if !slices.Contains(drafts, want) {
			t.Errorf("draft run: %s missing or without DraftMarker (have %v)", want, drafts)
		}
	}
	mustRun(t, root, 1, "holds draft output", "-check")

	mustRun(t, root, 0, "")
	for _, gone := range []string{androidRes + "/values-pt/strings_common.xml", androidRes + "/values-" + androidPseudoQualifier + "/strings_android.xml"} {
		if exists(root, gone) {
			t.Errorf("%s left behind by a draft run", gone)
		}
	}
	for _, kept := range []string{androidRes + "/values-es/strings_common.xml", androidRes + "/values/strings_app_photos.xml"} {
		if !exists(root, kept) {
			t.Errorf("%s not generated", kept)
		}
	}
	if got := read(t, root, androidRes+"/values/strings.xml"); got != androidHandWritten {
		t.Errorf("hand-written strings.xml changed:\n%s", got)
	}
	if got := read(t, root, androidKotlinDir+"/UiText.kt"); got != "// hand-written\n" {
		t.Errorf("hand-written UiText.kt changed")
	}
	mustRun(t, root, 0, "generated files checked", "-check")
}

func TestAndroidHandWrittenName(t *testing.T) {
	files := map[string]string{
		model.LanguagesFile: `{"languages": [{"code": "en", "tag": "en", "apple": "en", "android": "", "name": "English", "status": "shipping"}],
 "routes": {"app": ["android"]}}`,
		"i18n/strings/en/app.json":         `{"app.name": {"text": "Name", "note": "x"}}`,
		androidRes + "/values/strings.xml": androidHandWritten,
	}
	root := writeRepo(t, files)
	c, err := model.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = androidEmitter{}.Emit(c, Options{Root: root, Languages: c.OutputLanguages(false, nil)})
	if err == nil || !strings.Contains(err.Error(), "app_name is already used by the hand-written "+androidRes+"/values/strings.xml") {
		t.Errorf("collision with app_name: %v", err)
	}
}

// androidHostileRepo writes a catalog that skips Check; name is the English
// name as JSON spells it.
func androidHostileRepo(t *testing.T, name, entry string) *model.Catalog {
	t.Helper()
	root := writeRepo(t, map[string]string{
		model.LanguagesFile: `{"languages": [{"code": "en", "tag": "en", "apple": "en", "android": "", "name": "` + name + `", "status": "shipping"}],
 "routes": {"android": ["android"]}}`,
		"i18n/strings/en/android.json": `{` + entry + `}`,
	})
	c, err := model.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAndroidHostile(t *testing.T) {
	en := func(c *model.Catalog) Options { return Options{Languages: c.OutputLanguages(false, nil)} }
	// Text that doesn't tokenize is refused.
	for _, text := range []string{`</script><img onerror>`, `{{.}}`, `<b onclick=x>`, `{x}`} {
		c := androidHostileRepo(t, "English", `"android.x": {"text": "`+strings.ReplaceAll(text, `"`, `\"`)+`", "note": "n"}`)
		if _, err := (androidEmitter{}).Emit(c, en(c)); err == nil {
			t.Errorf("%q: no error", text)
		}
	}
	// Text that does comes out inert: escaped data in the XML, and in
	// Kotlin only inside a comment that it can't close.
	c := androidHostileRepo(t, `Eng\"$x\\`, `"android.x": {"text": "\"\"\" %s%n $& {ok} */ fun pwned() = 1 /* @x", "args": [["ok", "text"]], "note": "*/ fun pwned2() {} /*\nline"}`)
	out, err := androidEmitter{}.Emit(c, en(c))
	if err != nil {
		t.Fatal(err)
	}
	xml := string(out[androidRes+"/values/strings_android.xml"])
	if !strings.Contains(xml, `<string name="android_x">\"\"\" %%s%%n $&amp; %1$s */ fun pwned() = 1 /* @x</string>`) {
		t.Errorf("hostile XML:\n%s", xml)
	}
	kt := string(out[androidKotlinDir+"/S_Android.kt"])
	doc := kt[strings.Index(kt, "/**"):]
	doc = doc[:strings.Index(doc, "\n")]
	if strings.Count(doc, "*/") != 1 || !strings.HasSuffix(doc, " */") || strings.Count(doc, "/*") != 1 {
		t.Errorf("a comment can be closed or opened by the text:\n%s", doc)
	}
	if strings.Contains(kt, "\nline") {
		t.Errorf("a line break escaped the comment:\n%s", kt)
	}
	if langs := string(out[androidLanguages]); !strings.Contains(langs, `name = "Eng\"\$x\\"`) {
		t.Errorf("hostile language name:\n%s", langs)
	}
	// Names that can't become Kotlin or resource names are refused.
	for _, entry := range []string{
		`"android.x": {"text": "{in}", "args": [["in", "text"]], "note": "n"}`,
		`"android.x": {"text": "{A}", "args": [["A", "text"]], "note": "n"}`,
		`"android.x": {"text": "{n}", "args": [["n", "bytes"]], "note": "n"}`,
		`"android.x": {"text": {"one": "a", "other": "b"}, "note": "n"}`,
		`"android.x": {"text": "{n}", "args": [["n", "count"]], "note": "n"}`,
	} {
		c := androidHostileRepo(t, "English", entry)
		if _, err := (androidEmitter{}).Emit(c, en(c)); err == nil {
			t.Errorf("%s: no error", entry)
		}
	}
	// A qualifier that would leave res/ is refused.
	c = androidHostileRepo(t, "English", `"android.x": {"text": "x", "note": "n"}`)
	evil := model.Language{Code: "es", Tag: "es", Android: "../../x", Name: "x"}
	if _, err := (androidEmitter{}).Emit(c, Options{Languages: []model.Language{c.AllLanguages()[0], evil}}); err == nil {
		t.Error("qualifier ../../x: no error")
	}
}
