// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n/model"
)

// TestAndroidAapt2 hands the emitter's resources to the Android SDK's
// aapt2 - the fixture catalog in every language and the pseudo-locale, and
// edge cases Check would refuse - and compares every compiled value with
// what the runtime must get: the text as written, %N$s for each argument
// (between FSI and PDI for a user one), %% for a % when the key has
// arguments, tags as plain text. It is skipped without the SDK
// (ANDROID_HOME, or the default location).
func TestAndroidAapt2(t *testing.T) {
	aapt2, jar := androidSDK(t)
	dir := t.TempDir()

	c := androidLoadFixture(t)
	edge := androidEdgeCatalog(t)
	type set struct {
		c     *model.Catalog
		langs []model.Language
	}
	sets := []set{
		{c, androidAllLanguages(c)},
		{edge, edge.OutputLanguages(false, nil)},
	}
	var zips []string
	for i, s := range sets {
		res := filepath.Join(dir, fmt.Sprintf("res%d", i))
		for p, data := range androidEmit(t, s.c, Options{Languages: s.langs}) {
			rel, ok := strings.CutPrefix(p, androidRes+"/")
			if !ok || strings.HasPrefix(rel, "xml/") {
				continue
			}
			write(t, res, rel, string(data))
		}
		zip := filepath.Join(dir, fmt.Sprintf("res%d.zip", i))
		androidRun(t, aapt2, "compile", "--dir", res, "-o", zip)
		zips = append(zips, zip)
	}
	write(t, dir, "AndroidManifest.xml", `<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="cloud.offthe.otc.test"><application/></manifest>`)
	apk := filepath.Join(dir, "a.apk")
	androidRun(t, aapt2, append([]string{"link", "-I", jar, "--manifest", filepath.Join(dir, "AndroidManifest.xml"), "-o", apk}, zips...)...)
	got := androidParseDump(t, androidRun(t, aapt2, "dump", "resources", apk))

	want := map[string]string{}
	for _, s := range sets {
		for _, l := range s.langs {
			cfg := androidQualifier(l)
			for _, e := range s.c.EntriesFor(model.ConsumerAndroid) {
				r, _ := s.c.Resolve(l, e.Key)
				if !r.Translate && !l.IsSource() {
					continue
				}
				name := model.AndroidName(e.Key)
				if !r.Text.Plural {
					want["string/"+name+" ("+cfg+")"] = androidRuntime(e, r.Text.Other)
					continue
				}
				one, _ := androidOneForm(l, r)
				want["plurals/"+name+" ("+cfg+") one"] = androidRuntime(e, one)
				want["plurals/"+name+" ("+cfg+") other"] = androidRuntime(e, r.Text.Other)
				if androidManyLanguages[androidBase(l.Tag)] {
					want["plurals/"+name+" ("+cfg+") many"] = androidRuntime(e, r.Text.Other)
				}
			}
		}
	}
	var keys []string
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if g, ok := got[k]; !ok {
			t.Errorf("%s: not in the compiled resources", k)
		} else if g != want[k] {
			t.Errorf("%s: aapt2 compiled %q, want %q", k, g, want[k])
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: compiled but not expected", k)
		}
	}
	t.Logf("%d compiled values compared", len(want))
	if len(want) < 100 {
		t.Errorf("only %d values compared", len(want))
	}
}

// androidEdgeCatalog is text Check refuses (spaces aapt2 would trim or
// collapse, invisible characters) or that is merely awkward, written
// straight to a catalog.
func androidEdgeCatalog(t *testing.T) *model.Catalog {
	t.Helper()
	bs := "\\"
	type entry struct {
		Text any        `json:"text"`
		Args [][]string `json:"args,omitempty"`
		Note string     `json:"note"`
		Rich []string   `json:"rich,omitempty"`
	}
	plain := func(s string, args ...string) entry {
		e := entry{Text: s, Note: "edge"}
		for i := 0; i+1 < len(args); i += 2 {
			e.Args = append(e.Args, []string{args[i], args[i+1]})
		}
		return e
	}
	entries := map[string]entry{
		"android.edge.spaces":      plain(" a  b "),
		"android.edge.space_lines": plain("a \nb\n c "),
		"android.edge.tab":         plain("a\tb\t"),
		"android.edge.invisible":   plain("x" + string(rune(0x200d)) + "y" + string(rune(0x200e)) + "z" + string(rune(0xad)) + "w"),
		"android.edge.spaces_uni":  plain("a" + string(rune(0xa0)) + string(rune(0xa0)) + "b" + string(rune(0x3000)) + "c" + string(rune(0x2028)) + "d"),
		"android.edge.reference":   plain("@string/app_name"),
		"android.edge.attribute":   plain("?android:attr/textColor"),
		"android.edge.hostile":     plain(`""" %s%n $& ' \ %% %1$s`),
		"android.edge.hostile_arg": plain(`""" %s%n $& ' \ %% %1$s {a}`, "a", "text"),
		"android.edge.markup":      plain("&amp; &#60; --> ]]> a>b"),
		"android.edge.escapes":     plain(bs + "u0041 " + bs + "n " + bs + "@ " + bs + "?"),
		"android.edge.private":     plain("x" + string(rune(0xe000))),
		"android.edge.args":        plain("{c} {b} {a}", "a", "user", "b", "int", "c", "text"),
		"android.edge.rich":        {Text: "<b>{a}</b> <i>%</i>", Args: [][]string{{"a", "user"}}, Note: "edge", Rich: []string{"b", "i"}},
		"android.edge.plural":      {Text: map[string]string{"one": " one ", "other": "{n}  others % "}, Args: [][]string{{"n", "count"}}, Note: "edge"},
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	root := writeRepo(t, map[string]string{
		model.LanguagesFile: `{"languages": [{"code": "en", "tag": "en", "apple": "en", "android": "", "name": "English", "status": "shipping"},
 {"code": "es", "tag": "es", "apple": "es", "android": "es", "name": "Español", "status": "shipping"}],
 "routes": {"android": ["android"]}}`,
		"i18n/strings/en/android.json": string(data),
	})
	c, err := model.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// androidRuntime is the template a form must compile to.
func androidRuntime(e *model.Entry, form string) string {
	toks, err := model.Tokenize(form)
	if err != nil {
		return "tokenize: " + err.Error()
	}
	var b strings.Builder
	for _, t := range toks {
		switch t.Kind {
		case model.Literal:
			if len(e.Args) > 0 {
				b.WriteString(strings.ReplaceAll(t.Value, "%", "%%"))
			} else {
				b.WriteString(t.Value)
			}
		case model.Placeholder:
			i := e.ArgIndex(t.Value)
			spec := fmt.Sprintf("%%%d$s", i+1)
			if e.Args[i].Type == model.ArgUser {
				spec = androidTestFSI + spec + androidTestPDI
			}
			b.WriteString(spec)
		case model.OpenTag:
			b.WriteString("<" + t.Value + ">")
		case model.CloseTag:
			b.WriteString("</" + t.Value + ">")
		}
	}
	return b.String()
}

// androidSDK finds aapt2 and android.jar, or skips the test.
func androidSDK(t *testing.T) (string, string) {
	t.Helper()
	var homes []string
	for _, v := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if h := os.Getenv(v); h != "" {
			homes = append(homes, h)
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, filepath.Join(h, "Library", "Android", "sdk"), filepath.Join(h, "Android", "Sdk"))
	}
	last := func(pattern string) string {
		m, _ := filepath.Glob(pattern)
		sort.Strings(m)
		if len(m) == 0 {
			return ""
		}
		return m[len(m)-1]
	}
	for _, h := range homes {
		aapt2 := last(filepath.Join(h, "build-tools", "*", "aapt2"))
		jar := last(filepath.Join(h, "platforms", "android-*", "android.jar"))
		if aapt2 != "" && jar != "" {
			return aapt2, jar
		}
	}
	t.Skip("no Android SDK (aapt2 and android.jar): set ANDROID_HOME")
	return "", ""
}

func androidRun(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", path.Base(name), args[0], err, out)
	}
	return string(out)
}

var (
	androidDumpResource = regexp.MustCompile(`^    resource 0x[0-9a-f]+ ([a-z]+/[a-z0-9_]+)$`)
	androidDumpString   = regexp.MustCompile(`^      \(([^)]*)\) "(.*)$`)
	androidDumpPlurals  = regexp.MustCompile(`^      \(([^)]*)\) \(plurals\) size=\d+$`)
	androidDumpItem     = regexp.MustCompile(`^        (zero|one|two|few|many|other)="(.*)$`)
)

// androidParseDump reads aapt2 dump resources into "string/name (cfg)" and
// "plurals/name (cfg) quantity" -> value. A value's line breaks come out as
// is, the next line indented like the value's first.
func androidParseDump(t *testing.T, dump string) map[string]string {
	t.Helper()
	out := map[string]string{}
	var res, cfg, key, indent string
	var val []string
	flush := func() {
		if key != "" {
			v := strings.Join(val, "\n")
			if !strings.HasSuffix(v, `"`) {
				t.Errorf("dump: %s: value %q doesn't end with a quote", key, v)
			}
			out[key] = strings.TrimSuffix(v, `"`)
		}
		key, val = "", nil
	}
	for _, line := range strings.Split(dump, "\n") {
		switch m := []string(nil); {
		case strings.HasPrefix(line, "  type ") || line == "":
			flush()
			res = ""
		case androidDumpResource.MatchString(line):
			flush()
			res = androidDumpResource.FindStringSubmatch(line)[1]
		case res == "":
		case androidDumpPlurals.MatchString(line):
			flush()
			cfg = androidDumpPlurals.FindStringSubmatch(line)[1]
		case strings.HasPrefix(res, "plurals/") && androidDumpItem.MatchString(line):
			flush()
			m = androidDumpItem.FindStringSubmatch(line)
			key, val, indent = res+" ("+cfg+") "+m[1], []string{m[2]}, "        "
		case strings.HasPrefix(res, "string/") && androidDumpString.MatchString(line):
			flush()
			m = androidDumpString.FindStringSubmatch(line)
			key, val, indent = res+" ("+m[1]+")", []string{m[2]}, "      "
		case key != "" && strings.HasPrefix(line, indent):
			val = append(val, strings.TrimPrefix(line, indent))
		}
	}
	flush()
	return out
}
