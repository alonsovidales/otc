// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import (
	"bytes"
	stdlog "log"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// The test catalog: what the Go emitter writes for i18n/testdata/src
// (i18ngen's TestGoRuntimeFixture keeps testdata/catalog current).
var fixtureLangs = []Language{
	{Code: "en", Tag: "en", Name: "English"},
	{Code: "es", Tag: "es", Name: "Español"},
	{Code: "fr", Tag: "fr", Name: "Français"},
	{Code: "de", Tag: "de", Name: "Deutsch"},
	{Code: "pt", Tag: "pt-PT", Name: "Português"},
}

var fixture = newBundle(fixtureLangs, os.DirFS("testdata"))

// The languages above are the ones the emitter wrote for the fixture.
func TestFixtureLanguages(t *testing.T) {
	gen, err := os.ReadFile("cmd/i18ngen/testdata/go/languages_gen.go.golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range fixtureLangs {
		if want := `{Code: "` + l.Code + `", Tag: "` + l.Tag + `", Name: "` + l.Name + `"}`; !bytes.Contains(gen, []byte(want)) {
			t.Errorf("the fixture's languages lack %s", want)
		}
	}
}

func iso(s string) string { return fsi + s + pdi }

func TestRender(t *testing.T) {
	ana := map[string]any{"name": "Ana"}
	for _, tc := range []struct {
		lang, key string
		args      map[string]any
		want      string
	}{
		{"en", "common.cancel", nil, "Cancel"},
		{"es", "common.cancel", nil, "Cancelar"},
		{"ES-mx", "common.cancel", nil, "Cancelar"},
		{"es_419", "common.save", nil, "Guardar"},
		// Plurals by the canonical tag: French 0 is "one", pt-PT 0 is
		// not, and no language has a "many" for a million.
		{"en", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 1}, iso("Ana") + " deleted a photo"},
		{"en", "dev.photos_deleted", map[string]any{"name": "Ana", "count": int64(0)}, iso("Ana") + " deleted 0 photos"},
		{"en", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 1234}, iso("Ana") + " deleted 1,234 photos"},
		{"es", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 1}, iso("Ana") + " borró una foto"},
		{"es", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 1000000}, iso("Ana") + " borró 1.000.000 fotos"},
		{"fr", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 0}, iso("Ana") + " a supprimé 0 photo"},
		{"fr", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 1}, iso("Ana") + " a supprimé 1 photo"},
		{"fr", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 2}, iso("Ana") + " a supprimé 2 photos"},
		{"pt", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 0}, iso("Ana") + " apagou 0 fotos"},
		{"pt-BR", "dev.photos_deleted", map[string]any{"name": "Ana", "count": 1}, iso("Ana") + " apagou uma foto"},
		{"en", "dev.photos_deleted", map[string]any{"name": "Ana", "count": -1}, iso("Ana") + " deleted a photo"},
		// Numbers as each language writes them.
		{"es", "dev.files_left", map[string]any{"done": 1234, "total": 5000}, "1.234 de 5.000 archivos"},
		// No French text: the English, with English plural rules and
		// numbers ("0 files", where French rules would pick "one").
		{"fr", "desk.syncing", map[string]any{"count": 1234}, "Syncing 1,234 files"},
		{"fr", "desk.syncing", map[string]any{"count": 0}, "Syncing 0 files"},
		{"es", "desk.syncing", map[string]any{"count": 1}, "Sincronizando 1 archivo"},
		{"en", "dev.free_space", map[string]any{"free": int64(820_000_000), "total": int64(4_560_000_000)}, "820 MB free of 4.6 GB"},
		{"fr", "dev.free_space", map[string]any{"free": int64(1500), "total": int64(984_000_000_000)}, "1,5\u00a0ko libres sur 984\u00a0Go"},
		// A msg argument is another key, in the same language.
		{"en", "dev.disk_failed", map[string]any{"port": "top", "reason": "dev.reason_unplugged"}, "The top disk stopped working: it was unplugged"},
		{"es", "dev.disk_failed", map[string]any{"port": "top", "reason": "dev.reason_unplugged"}, "El disco top dejó de funcionar: se desconectó"},
		// Fallbacks: a missing or stale translation, an unknown language
		// or key.
		{"es", "dev.only_en", nil, "Only in English"},
		{"es", "dev.stale", nil, "The new English"},
		{"nl", "common.cancel", nil, "Cancel"},
		{"", "common.cancel", nil, "Cancel"},
		{"es-ES-valencia", "common.cancel", nil, "Cancel"}, // longer than 8: not a language
		{"es", "dev.no_such_key", nil, "dev.no_such_key"},
		// Text special to templates, printf or a replacement string is
		// plain text, in the template and in the arguments.
		{"en", "dev.literal", ana, `100% of ` + iso("Ana") + `'s "space" & $1 $& %s%n \ done`},
		{"es", "dev.literal", map[string]any{"name": "{name} $& %s <b>{count}</b>"}, `El 100% del "espacio" de ` + iso("{name} $& %s <b>{count}</b>") + ` & $1 $& %s%n \ hecho`},
		// A rich key as plain text loses its tags.
		{"es", "site.privacy", ana, "Lee la política de privacidad, " + iso("Ana") + "."},
	} {
		if got := fixture.render(tc.lang, tc.key, tc.args); got != tc.want {
			t.Errorf("%s %s %v:\n got %q\nwant %q", tc.lang, tc.key, tc.args, got, tc.want)
		}
	}
}

// An argument is never scanned again: one that looks like a placeholder
// stays as it is, even where the same placeholder comes next.
func TestSinglePass(t *testing.T) {
	got := fixture.render("en", "dev.files_left", map[string]any{"done": int64(1), "total": int64(2)})
	if got != "1 of 2 files" {
		t.Fatal(got)
	}
	got = fixture.render("en", "dev.disk_failed", map[string]any{"port": "{reason}", "reason": "dev.reason_unplugged"})
	if got != "The {reason} disk stopped working: it was unplugged" {
		t.Errorf("got %q", got)
	}
}

func TestIsolation(t *testing.T) {
	// Directional controls in a name would end or override its isolation.
	got := fixture.render("en", "dev.literal", map[string]any{"name": "a\u2069b\u202ec\u2066d\u202ae"})
	if want := "100% of " + iso("abcde") + `'s "space" & $1 $& %s%n \ done`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// text arguments are the app's own: not isolated.
	if got := fixture.render("en", "push.update_ready", map[string]any{"version": "118"}); got != "Update 118 is ready" {
		t.Errorf("got %q", got)
	}
	// A right-to-left name stays inside its marks.
	if got := fixture.render("es", "dev.photos_deleted", map[string]any{"name": "שרה", "count": 2}); got != iso("שרה")+" borró 2 fotos" {
		t.Errorf("got %q", got)
	}
}

func TestDatetime(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 5, 0, 0, time.UTC)
	for lang, want := range map[string]string{
		"en": "1 Oct 2026, 09:05",
		"es": "1 oct 2026, 09:05",
		"fr": "1 oct. 2026, 09:05",
		"de": "01.10.2026, 09:05",
		"pt": "01/10/2026, 09:05",
	} {
		if got := fixture.formatTime(fixture.lang(lang), at); got != want {
			t.Errorf("%s: %q, want %q", lang, got, want)
		}
	}
	// A datetime argument is unix seconds, shown in the machine's zone.
	want := "Zuletzt gesehen am " + fixture.formatTime(3, time.Unix(at.Unix(), 0))
	for _, v := range []any{at.Unix(), at, float64(at.Unix())} {
		if got := fixture.render("de", "dev.last_seen", map[string]any{"at": v}); got != want {
			t.Errorf("%T: %q, want %q", v, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		lang string
		n    int64
		want string
	}{
		{"en", 0, "0 B"},
		{"en", 999, "999 B"},
		{"en", 1000, "1.0 KB"},
		{"en", 1500, "1.5 KB"},
		{"en", 99_940, "99.9 KB"},
		{"en", 99_960, "100 KB"},
		{"en", 999_400, "999 KB"},
		{"en", 999_600, "1.0 MB"},
		{"en", 4_600_000_000, "4.6 GB"},
		{"en", 984_000_000_000, "984 GB"},
		{"en", 2_500_000_000_000_000_000, "2,500 PB"},
		{"es", 4_600_000_000, "4,6\u00a0GB"},
		{"de", 1234, "1,2\u00a0KB"},
		{"fr", 820_000_000, "820\u00a0Mo"},
		{"pt", 500, "500\u00a0B"},
	} {
		if got := fixture.formatBytes(fixture.lang(tc.lang), tc.n); got != tc.want {
			t.Errorf("%s %d: %q, want %q", tc.lang, tc.n, got, tc.want)
		}
	}
}

// A missing argument, or one of the wrong type, comes out empty - never a
// literal {name} - and the rest of the text still renders.
func TestBadArguments(t *testing.T) {
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{nil, "The  disk stopped working: "},
		{map[string]any{"port": 3, "reason": "dev.reason_unplugged"}, "The  disk stopped working: it was unplugged"},
		{map[string]any{"port": "top", "reason": "dev.no_such_key"}, "The top disk stopped working: "},
		{map[string]any{"port": "top", "reason": "dev.reason_plugged"}, "The top disk stopped working: "}, // has arguments
		{map[string]any{"port": "top", "reason": "site.privacy"}, "The top disk stopped working: "},       // has tags
		{map[string]any{"port": "top", "reason": "dev.photos_deleted"}, "The top disk stopped working: "},
	} {
		if got := fixture.render("en", "dev.disk_failed", tc.args); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.args, got, tc.want)
		}
	}
	for _, v := range []any{"3", 2.5, uint64(1 << 63), nil, []int{1}} {
		got := fixture.render("en", "dev.files_left", map[string]any{"done": v, "total": 2})
		if got != " of 2 files" {
			t.Errorf("%T %v: %q", v, v, got)
		}
	}
	// No count: the "other" form.
	if got := fixture.render("en", "dev.photos_deleted", map[string]any{"name": "Ana"}); got != iso("Ana")+" deleted  photos" {
		t.Errorf("%q", got)
	}
}

func TestParts(t *testing.T) {
	for _, tc := range []struct {
		lang string
		want []Part
	}{
		{"en", []Part{{"", "Read the "}, {"link", "privacy policy"}, {"", ", " + iso("Ana") + "."}}},
		{"es", []Part{{"", "Lee la "}, {"link", "política de privacidad"}, {"", ", " + iso("Ana") + "."}}},
	} {
		got := fixture.parts(tc.lang, "site.privacy", map[string]any{"name": "Ana"})
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %q", tc.lang, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: %q, want %q", tc.lang, got, tc.want)
			}
		}
	}
	if got := fixture.parts("en", "common.save", nil); len(got) != 1 || got[0] != (Part{Text: "Save"}) {
		t.Errorf("plain key: %q", got)
	}
	if got := fixture.parts("en", "dev.nope", nil); len(got) != 1 || got[0].Text != "dev.nope" {
		t.Errorf("unknown key: %q", got)
	}
}

// The catalog files decide nothing a broken file could abuse: a
// translation that doesn't parse, doesn't match the English shape or
// uses an undeclared argument is left out, and the English stands in.
func TestBrokenCatalogFallsBack(t *testing.T) {
	fsys := fstest.MapFS{
		"catalog/en/x.json": {Data: []byte(`{
  "@draft": "OTC-I18N-DRAFT-OUTPUT-DO-NOT-COMMIT",
  "dev.a": {"text": "A {n}", "args": [["n", "int"]]},
  "dev.b": {"text": {"one": "one", "other": "{count} b"}, "args": [["count", "count"]]},
  "dev.c": {"text": "C {x}"},
  "dev.d": {"text": "D", "args": [["n", "float"]]},
  "dev.e": {"text": "<b>E</b>"},
  "dev.f": {"text": "F"}
}`)},
		"catalog/es/x.json": {Data: []byte(`{
  "dev.a": {"text": "A {m}"},
  "dev.b": {"text": "plain"},
  "dev.e": {"text": "E sin etiqueta"},
  "dev.f": {"text": "F {{.}}"},
  "dev.g": {"text": "no English"}
}`)},
		"catalog/es/broken.json": {Data: []byte(`{"dev.z": `)},
	}
	b := newBundle(fixtureLangs[:2], fsys)
	for key, want := range map[string]string{
		"dev.a": "A 1",
		"dev.b": "1 b",
		"dev.c": "dev.c", // undeclared argument in English: no such key
		"dev.d": "dev.d", // unknown type
		"dev.e": "E",
		"dev.f": "F",
		"dev.g": "dev.g",
	} {
		args := map[string]any{"n": 1, "count": 1}
		if key == "dev.b" {
			args["count"] = 1
			want = "one"
			if got := b.render("es", key, map[string]any{"count": 2}); got != "2 b" {
				t.Errorf("es dev.b with 2: %q", got)
			}
		}
		if got := b.render("es", key, args); got != want {
			t.Errorf("es %s: %q, want %q", key, got, want)
		}
	}
	if b.table(0).msgs[draftKey] != nil {
		t.Error("the draft marker became a key")
	}
}

func TestStored(t *testing.T) {
	m := Msg{Key: "dev.photos_deleted", Args: map[string]any{"name": "Ana", "count": int64(3)}}
	raw, err := m.MarshalArgs()
	if err != nil || string(raw) != `{"count":3,"name":"Ana"}` {
		t.Fatalf("MarshalArgs: %s %v", raw, err)
	}
	back, err := fixture.parseStored(m.Key, raw)
	if err != nil || back.Args["count"] != int64(3) || back.Args["name"] != "Ana" {
		t.Fatalf("parseStored: %v %v", back, err)
	}
	if raw, _ := (Msg{Key: "dev.only_en"}).MarshalArgs(); string(raw) != "{}" {
		t.Errorf("no arguments: %s", raw)
	}
	for _, tc := range []struct {
		key, args string
		ok        bool
	}{
		{"dev.photos_deleted", `{"name": "Ana", "count": 3}`, true},
		{"dev.photos_deleted", ` {"count": 3, "name": "<script>"} `, true},
		{"dev.only_en", ``, true},
		{"dev.only_en", `null`, true},
		{"dev.only_en", `{}`, true},
		{"dev.disk_failed", `{"port": "top", "reason": "dev.reason_unplugged"}`, true},
		{"dev.last_seen", `{"at": 1790000000}`, true},
		{"dev.no_such_key", `{}`, false},
		{"", `{}`, false},
		{"dev.photos_deleted", `{"name": "Ana"}`, false},
		{"dev.photos_deleted", `{"name": "Ana", "count": 3, "extra": 1}`, false},
		{"dev.photos_deleted", `{"name": "Ana", "cnt": 3}`, false},
		{"dev.photos_deleted", `{"name": 7, "count": 3}`, false},
		{"dev.photos_deleted", `{"name": "Ana", "count": "3"}`, false},
		{"dev.photos_deleted", `{"name": "Ana", "count": 3.5}`, false},
		{"dev.photos_deleted", `{"name": "Ana", "count": 1e3}`, false},
		{"dev.photos_deleted", `{"name": "Ana", "count": 99999999999999999999}`, false},
		{"dev.photos_deleted", `{"name": null, "count": 3}`, false},
		{"dev.photos_deleted", `{"name": "Ana", "count": 3} {}`, false},
		{"dev.photos_deleted", `{"name": "Ana", "count": 3`, false},
		{"dev.photos_deleted", `["Ana", 3]`, false},
		{"dev.only_en", `{"x": 1}`, false},
		{"dev.only_en", `"x"`, false},
		{"dev.disk_failed", `{"port": "top", "reason": "dev.reason_plugged"}`, false},
		{"dev.disk_failed", `{"port": "top", "reason": "dev.nope"}`, false},
		{"dev.disk_failed", `{"port": "top", "reason": 1}`, false},
	} {
		_, err := fixture.parseStored(tc.key, []byte(tc.args))
		if (err == nil) != tc.ok {
			t.Errorf("%s %s: %v", tc.key, tc.args, err)
		}
	}
}

func TestRenderStored(t *testing.T) {
	args := []byte(`{"name": "Ana", "count": 2}`)
	stored := "Ana deleted 2 photos"
	for _, tc := range []struct {
		lang, key string
		args      []byte
		english   string
		want      string
	}{
		{"es", "dev.photos_deleted", args, stored, iso("Ana") + " borró 2 fotos"},
		{"", "dev.photos_deleted", args, stored, stored},   // no lang: today's text, byte for byte
		{"en", "dev.photos_deleted", args, stored, stored}, // English: what was written
		{"ja", "dev.photos_deleted", args, stored, stored}, // a language without text
		{"es", "", args, stored, stored},                   // a row from before keys
		{"es", "dev.unknown", args, stored, stored},        // a newer release's key
		{"es", "dev.photos_deleted", []byte(`{"name": "Ana"}`), stored, stored},
		{"es", "dev.photos_deleted", []byte(`{bad`), stored, stored},
		{"en", "dev.photos_deleted", args, "", iso("Ana") + " deleted 2 photos"}, // nothing stored: the catalog's
	} {
		if got := fixture.renderStored(tc.lang, tc.key, tc.args, tc.english); got != tc.want {
			t.Errorf("%s %s %s: %q, want %q", tc.lang, tc.key, tc.args, got, tc.want)
		}
	}
}

func TestCheck(t *testing.T) {
	ok := map[string]map[string]any{
		"dev.photos_deleted": {"name": "Ana", "count": 3},
		"dev.last_seen":      {"at": time.Now()},
		"dev.free_space":     {"free": uint32(1), "total": int64(2)},
		"common.cancel":      nil,
	}
	for key, args := range ok {
		if err := fixture.check(key, args); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
	if err := fixture.check("dev.photos_deleted", map[string]any{"name": "s3cret-value"}); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("missing count: %v", err)
	}
	if err := fixture.check("dev.nope", nil); err != ErrUnknownKey {
		t.Errorf("unknown key: %v", err)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"en": "en", "es": "es", "ES": "es", "es-MX": "es", "es_ES": "es", "pt-PT": "pt", "pt-BR": "pt", "Pt_br": "pt",
		"fr-CA": "fr", "es-419": "es",
		"": "", "nl": "", "ja": "", "xx": "", "e": "", "es-": "es", "-es": "",
		"es-ES-valencia": "", "es-123456": "", "es mx": "", "es;q=1": "", "es\n": "", "../es": "", "es\x00": "",
		"ñ": "", "esp": "", "qps": "",
	} {
		if got := fixture.normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidCode(t *testing.T) {
	for in, want := range map[string]bool{"es": true, "ast": true, "": false, "e": false, "espa": false, "ES": false, "e1": false, "pt-PT": false} {
		if ValidCode(in) != want {
			t.Errorf("ValidCode(%q) = %v", in, !want)
		}
	}
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		prefs []string
		want  string
	}{
		{[]string{"es-MX"}, "es"},
		{[]string{"pt-BR"}, "pt"},
		{[]string{"pt"}, "pt"},
		{[]string{"de-CH"}, "de"},
		{[]string{"ca-ES", "es-ES"}, "es"},
		{[]string{"ca-ES,es-ES;q=0.8"}, "es"},
		{[]string{"en-US,es;q=0.9"}, "en"},
		{[]string{"es;q=0.5,fr;q=0.9"}, "fr"},
		{[]string{"es;q=0,fr"}, "fr"},
		{[]string{"gl-ES"}, "en"}, // related, but not the same language
		{[]string{"eu"}, "en"},
		{[]string{"ja-JP"}, "en"},
		{[]string{"nl-BE"}, "en"}, // not in this build
		{[]string{"*"}, "en"},
		{[]string{""}, "en"},
		{nil, "en"},
		{[]string{"es_ES.UTF-8"}, "es"},
		{[]string{"fr_FR@euro"}, "fr"},
		{[]string{"C.UTF-8", "de_DE.UTF-8"}, "de"},
		{[]string{"ja:fr:en"}, "fr"},
		{[]string{"POSIX"}, "en"},
		{[]string{"garbage;;;", "pt_PT"}, "pt"},
		{[]string{strings.Repeat("es,", 400)}, "en"},
	} {
		if got := fixture.match(tc.prefs); got != tc.want {
			t.Errorf("match(%q) = %q, want %q", tc.prefs, got, tc.want)
		}
	}
}

// The log names a missing key or a key with bad arguments, once, and
// never an argument.
func TestNeverLogsArguments(t *testing.T) {
	var buf bytes.Buffer
	stdlog.SetOutput(&buf)
	defer stdlog.SetOutput(os.Stderr)
	for i := 0; i < 3; i++ {
		fixture.render("es", "dev.logged_missing", map[string]any{"name": "s3cret-name"})
		fixture.render("es", "dev.free_space", map[string]any{"free": "s3cret-free", "total": 1})
		fixture.renderStored("es", "dev.logged_stored", []byte(`{"name": "s3cret-stored"}`), "English")
	}
	out := buf.String()
	if strings.Contains(out, "s3cret") {
		t.Errorf("an argument reached the log:\n%s", out)
	}
	if strings.Count(out, `"dev.logged_missing"`) != 1 || strings.Count(out, `"dev.free_space"`) != 1 {
		t.Errorf("want each key logged once:\n%s", out)
	}
}

// Many goroutines render in every language at once (run with -race):
// tables load once, printers are shared.
func TestConcurrentRendering(t *testing.T) {
	b := newBundle(fixtureLangs, os.DirFS("testdata"))
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				l := fixtureLangs[(g+i)%len(fixtureLangs)].Code
				b.render(l, "dev.photos_deleted", map[string]any{"name": "Ana", "count": i})
				b.formatBytes(b.lang(l), int64(i)*1_000_003)
				b.parts(l, "site.privacy", map[string]any{"name": "Ana"})
			}
		}(g)
	}
	wg.Wait()
}
