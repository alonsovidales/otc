// SPDX-License-Identifier: AGPL-3.0-or-later

package alerttext

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/i18n"
)

// The English the device wrote before its alerts had keys, reproduced
// from the code that wrote it (files_manager.alert's "<name> <what>",
// integrityReport, memoryAlertTitle/Hint, setAside, bridge_leave,
// raidwatch, updateAlert, wifiwatch), next to the key that now writes it.
// Every dev.alert key is here: the English a new row stores is the old
// English, byte for byte.
func everyAlertKey() map[string]struct {
	m    i18n.Msg
	want string
} {
	const (
		name    = "IMG_0001.HEIC"
		paths   = "/kim/b.jpg; /photos/b.jpg; /photos/c.jpg"
		domain  = "kim.off-the.cloud"
		address = "http://192.168.1.20:8080"
		slow    = "0.17 MB/s, with 7.7% of it sent twice" // streak.describe()
		reboot  = " It didn't reconnect after the restart, so the device restarted."
		upload  = ". Upload them again from where they came from - Sync All on a phone, or the desktop app's folder - and they are restored."
		logs    = " If this keeps happening, send the logs from Settings > Logs."
		mirror  = "Your files are safe on the other disk, but they are no longer mirrored. Replace it with a card at least as large: the device adds it and copies everything onto it by itself, erasing whatever was on the new card."
		restart = " Restart the device from Settings, or connect it to your router with an Ethernet cable."
	)
	type c = struct {
		m    i18n.Msg
		want string
	}
	cases := map[string]c{
		"dev.alert.bridge.left":        {i18n.DevAlertBridgeLeft(), "This device left the bridge"},
		"dev.alert.bridge.left_detail": {i18n.DevAlertBridgeLeftDetail(domain, address), fmt.Sprintf("The bridge no longer knows %s: its account was deleted or the name released. The device goes on working at home, at %s - set the apps to that address, or join the bridge again in Settings.", domain, address)},

		"dev.alert.file.crashed":                     {i18n.DevAlertFileCrashed(name), name + " " + "could not be processed (it crashed the processing)"},
		"dev.alert.file.not_converted":               {i18n.DevAlertFileNotConverted(name), name + " " + "could not be converted from HEIC"},
		"dev.alert.file.not_decrypted":               {i18n.DevAlertFileNotDecrypted(name), name + " " + "could not be decrypted"},
		"dev.alert.file.not_processed":               {i18n.DevAlertFileNotProcessed(name), name + " " + "could not be processed"},
		"dev.alert.file.not_read":                    {i18n.DevAlertFileNotRead(name), name + " " + "could not be read"},
		"dev.alert.file.not_read_back":               {i18n.DevAlertFileNotReadBack(name), name + " " + "could not be read back for processing"},
		"dev.alert.file.not_saved":                   {i18n.DevAlertFileNotSaved(name), name + " " + "could not be saved to disk"},
		"dev.alert.file.not_tagged":                  {i18n.DevAlertFileNotTagged(name), name + " " + "could not be tagged"},
		"dev.alert.file.small_thumbnail_not_written": {i18n.DevAlertFileSmallThumbnailNotWritten(name), name + " " + "has no small thumbnail (it could not be written)"},
		"dev.alert.file.thumbnail_not_encoded":       {i18n.DevAlertFileThumbnailNotEncoded(name), name + " " + "has no thumbnail (it could not be encoded)"},
		"dev.alert.file.thumbnail_not_written":       {i18n.DevAlertFileThumbnailNotWritten(name), name + " " + "has no thumbnail (it could not be written)"},
		// safely("sharing a gallery of", fmt.Sprintf("%d files", n)): the
		// "file name" of a gallery's crash was its count of files.
		"dev.alert.gallery.crashed": {i18n.DevAlertGalleryCrashed(12), fmt.Sprintf("%d files", 12) + " " + "could not be processed (it crashed the processing)"},

		"dev.alert.integrity.lost":        {i18n.DevAlertIntegrityLost(3), fmt.Sprintf("%d files have lost their content", 3)},
		"dev.alert.integrity.detail":      {i18n.DevAlertIntegrityDetail(paths), paths + upload},
		"dev.alert.integrity.detail_more": {i18n.DevAlertIntegrityDetailMore(paths, 5), paths + fmt.Sprintf("; and %d more", 5) + upload},

		"dev.alert.line.detail": {i18n.DevAlertLineDetail("14:05", name+" could not be read", "EOF"), "14:05" + " " + name + " could not be read" + ": " + "EOF"},
		"dev.alert.line.plain":  {i18n.DevAlertLinePlain("14:05", name+" was set aside"), "14:05" + " " + name + " was set aside"},

		"dev.alert.memory.restarted":           {i18n.DevAlertMemoryRestarted(), "Your device ran out of memory and restarted"},
		"dev.alert.memory.restarted_times":     {i18n.DevAlertMemoryRestartedTimes(12, "19:34"), fmt.Sprintf("Your device ran out of memory and restarted %d times since %s", 12, "19:34")},
		"dev.alert.memory.restarted_times_day": {i18n.DevAlertMemoryRestartedTimesDay(3, 8, i18n.DevAlertMonthOct(), "19:34"), fmt.Sprintf("Your device ran out of memory and restarted %d times since %s", 3, time.Date(2026, 10, 8, 19, 34, 0, 0, time.Local).Format("2 Jan 15:04"))},
		"dev.alert.memory.hint_low_small":      {i18n.DevAlertMemoryHintLowSmall(4), fmt.Sprintf("This device has %d GB of memory", 4) + ", less than the 8 GB recommended: it processes one photo at a time to fit (low-memory mode), and after a restart it waits a while before it goes on." + logs},
		"dev.alert.memory.hint_low":            {i18n.DevAlertMemoryHintLow(8), fmt.Sprintf("This device has %d GB of memory", 8) + ": it processes one photo at a time (low-memory mode), and after a restart it waits a while before it goes on." + logs},
		"dev.alert.memory.hint_low_unknown":    {i18n.DevAlertMemoryHintLowUnknown(), "This device" + ": it processes one photo at a time (low-memory mode), and after a restart it waits a while before it goes on." + logs},
		"dev.alert.memory.hint":                {i18n.DevAlertMemoryHint(8), fmt.Sprintf("This device has %d GB of memory", 8) + ". After a restart it goes on processing one file at a time for an hour." + logs},
		"dev.alert.memory.hint_unknown":        {i18n.DevAlertMemoryHintUnknown(), "This device" + ". After a restart it goes on processing one file at a time for an hour." + logs},

		"dev.alert.raid.stopped_top":         {i18n.DevAlertRaidStoppedTop(), "The disk in the top blue USB port" + " stopped working"},
		"dev.alert.raid.stopped_bottom":      {i18n.DevAlertRaidStoppedBottom(), "The disk in the bottom blue USB port" + " stopped working"},
		"dev.alert.raid.stopped_unknown":     {i18n.DevAlertRaidStoppedUnknown(), "One of the two storage disks" + " stopped working"},
		"dev.alert.raid.stopped_detail":      {i18n.DevAlertRaidStoppedDetail(), mirror},
		"dev.alert.raid.stopped_detail_disk": {i18n.DevAlertRaidStoppedDetailDisk("sdb BBB xhci-hcd.1 usb4 4-1"), mirror + " (" + "sdb BBB xhci-hcd.1 usb4 4-1" + ")"},
		"dev.alert.raid.working_title":       {i18n.DevAlertRaidWorkingTitle(), "Both storage disks are working again"},
		"dev.alert.raid.working_detail":      {i18n.DevAlertRaidWorkingDetail(), "The mirror is complete: every file is on both disks again."},

		"dev.alert.setaside.title":  {i18n.DevAlertSetasideTitle(name), name + " was set aside"},
		"dev.alert.setaside.detail": {i18n.DevAlertSetasideDetail(), "the device stopped twice while processing it. It is kept as it is, without the thumbnail, tags or faces it was missing, and tried again after the next update."},

		"dev.alert.update.major":    {i18n.DevAlertUpdateMajor("3.0"), fmt.Sprintf("Update %s is available", "3.0")},
		"dev.alert.update.critical": {i18n.DevAlertUpdateCritical("3.0"), fmt.Sprintf("Critical update %s - please install it soon", "3.0")},
		"dev.alert.update.detail":   {i18n.DevAlertUpdateDetail("A security fix."), "A security fix." + " Install it from Settings."},

		"dev.alert.wifi.restarted":            {i18n.DevAlertWifiRestarted(), "The Wi-Fi was restarted"},
		"dev.alert.wifi.still_slow":           {i18n.DevAlertWifiStillSlow(), "The Wi-Fi is still slow after a restart"},
		"dev.alert.wifi.very_slow":            {i18n.DevAlertWifiVerySlow(), "The Wi-Fi is sending data very slowly"},
		"dev.alert.wifi.not_restarted":        {i18n.DevAlertWifiNotRestarted(), "The Wi-Fi could not be restarted"},
		"dev.alert.wifi.recovered":            {i18n.DevAlertWifiRecovered("0.17 MB/s", "7.7"), "sending data had slowed down to about " + slow + "." + "" + " It is working normally again."},
		"dev.alert.wifi.recovered_rebooted":   {i18n.DevAlertWifiRecoveredRebooted("0.17 MB/s", "7.7"), "sending data had slowed down to about " + slow + "." + reboot + " It is working normally again."},
		"dev.alert.wifi.unjudged":             {i18n.DevAlertWifiUnjudged("0.17 MB/s", "7.7"), "sending data had slowed down to about " + slow + "." + "" + " There hasn't been enough traffic since to tell whether it is working normally again."},
		"dev.alert.wifi.unjudged_rebooted":    {i18n.DevAlertWifiUnjudgedRebooted("0.17 MB/s", "7.7"), "sending data had slowed down to about " + slow + "." + reboot + " There hasn't been enough traffic since to tell whether it is working normally again."},
		"dev.alert.wifi.still_slow_detail":    {i18n.DevAlertWifiStillSlowDetail("0.17 MB/s", "7.7"), "sending data is still slow (about " + slow + "). The Wi-Fi won't be restarted again until it has worked normally for a while." + restart},
		"dev.alert.wifi.no_runner":            {i18n.DevAlertWifiNoRunner("140 KB/s", "7.7"), "sending data has slowed down to about " + "140 KB/s, with 7.7% of it sent twice" + ". This device can't restart its Wi-Fi by itself until it is updated." + restart},
		"dev.alert.wifi.restart_failed":       {i18n.DevAlertWifiRestartFailed("0.17 MB/s", "7.7", i18n.DevStatusWifiTooSoon()), "sending data had slowed down to about " + slow + ", and restarting the Wi-Fi failed (" + "the Wi-Fi was restarted less than 30 minutes ago" + ")." + restart},
		"dev.alert.wifi.restart_failed_other": {i18n.DevAlertWifiRestartFailedOther("0.17 MB/s", "7.7", "modprobe failed"), "sending data had slowed down to about " + slow + ", and restarting the Wi-Fi failed (" + "modprobe failed" + ")." + restart},
		"dev.alert.wifi.no_answer":            {i18n.DevAlertWifiNoAnswer("0.17 MB/s", "7.7"), "sending data had slowed down to about " + slow + ", and the Wi-Fi restart never answered." + restart},
	}
	months := []func() i18n.Msg{i18n.DevAlertMonthJan, i18n.DevAlertMonthFeb, i18n.DevAlertMonthMar, i18n.DevAlertMonthApr, i18n.DevAlertMonthMay, i18n.DevAlertMonthJun,
		i18n.DevAlertMonthJul, i18n.DevAlertMonthAug, i18n.DevAlertMonthSep, i18n.DevAlertMonthOct, i18n.DevAlertMonthNov, i18n.DevAlertMonthDec}
	for i, month := range months {
		m := month()
		cases[m.Key] = c{m, time.Date(2026, time.Month(i+1), 2, 0, 0, 0, 0, time.UTC).Format("Jan")}
	}
	return cases
}

func TestEveryAlertKeysEnglishIsTheOldEnglish(t *testing.T) {
	cases := everyAlertKey()
	for key, c := range cases {
		if c.m.Key != key {
			t.Errorf("%s: the case builds %s", key, c.m.Key)
		}
		if err := c.m.Check(); err != nil {
			t.Errorf("%s: %v", key, err)
		}
		if got := EnglishOf(c.m); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", key, got, c.want)
		}
	}
	// Every key of the file is checked, and every one an alert stores is
	// marked stored (rows written now must render for good).
	raw, err := os.ReadFile("../i18n/strings/en/dev.alert.json")
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]struct {
		Stored bool `json:"stored"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for key, e := range file {
		if _, ok := cases[key]; !ok {
			t.Errorf("%s has no English check here", key)
		}
		if layout := strings.HasPrefix(key, "dev.alert.line."); e.Stored == layout {
			t.Errorf("%s: stored %v (only the line layouts, never written to a row, are not)", key, e.Stored)
		}
	}
	if len(file) != len(cases) {
		t.Errorf("%d keys in the file, %d checked", len(file), len(cases))
	}
}

// One alert in English is one plural form or the other, as the code chose
// by hand: "1 file has lost its content".
func TestPluralFormsAreTheOldOnes(t *testing.T) {
	for _, c := range []struct {
		m    i18n.Msg
		want string
	}{
		{i18n.DevAlertIntegrityLost(1), "1 file has lost its content"},
		{i18n.DevAlertIntegrityLost(20), "20 files have lost their content"},
		{i18n.DevAlertIntegrityDetailMore("/f/a", 1), "/f/a; and 1 more. Upload them again from where they came from - Sync All on a phone, or the desktop app's folder - and they are restored."},
		{i18n.DevAlertGalleryCrashed(1), "1 files could not be processed (it crashed the processing)"},
		{i18n.DevAlertMemoryRestartedTimes(2, "19:35"), "Your device ran out of memory and restarted 2 times since 19:35"},
	} {
		if got := EnglishOf(c.m); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.m.Key, got, c.want)
		}
	}
}

// The English never had isolation marks around a file name; the runtime
// adds them (and drops directional controls from the name) in every
// rendering, EnglishOf takes the marks out again.
func TestEnglishOfDropsTheIsolationMarks(t *testing.T) {
	m := i18n.DevAlertFileNotRead("photo.jpg")
	if r := m.Render("en"); !strings.ContainsRune(r, fsi) || !strings.ContainsRune(r, pdi) {
		t.Fatalf("the runtime no longer isolates a name: %q", r)
	}
	if got := EnglishOf(m); got != "photo.jpg could not be read" {
		t.Errorf("EnglishOf = %q", got)
	}
	if got := EnglishOf(i18n.DevAlertFileNotRead("cod\u202Egpj.exe")); got != "codgpj.exe could not be read" {
		t.Errorf("a name with a directional override: %q", got)
	}
}

func TestNewAlert(t *testing.T) {
	a := New(i18n.DevAlertSetasideTitle("x.jpg"), i18n.DevAlertSetasideDetail())
	if a.Title != "x.jpg was set aside" || !strings.HasPrefix(a.Detail, "the device stopped twice") {
		t.Errorf("English %+v", a)
	}
	if a.Key != "dev.alert.setaside.title" || string(a.Args) != `{"name":"x.jpg"}` || a.DetailKey != "dev.alert.setaside.detail" || string(a.DetailArgs) != "{}" || !a.Keyed() {
		t.Errorf("keys %+v", a)
	}

	a = New(i18n.DevAlertMemoryRestarted(), i18n.Msg{})
	if a.Title != "Your device ran out of memory and restarted" || a.Detail != "" || a.DetailKey != "" || !a.Keyed() {
		t.Errorf("no detail: %+v", a)
	}

	a = WithRaw(i18n.DevAlertFileNotRead("x.jpg"), "open x: no such file")
	if a.Title != "x.jpg could not be read" || a.Detail != "open x: no such file" || a.DetailKey != "" || a.Key != "dev.alert.file.not_read" {
		t.Errorf("raw detail: %+v", a)
	}

	// A message that doesn't fit its key is a mistake in the code: the
	// alert keeps its English only, never a key that can't render.
	a = New(i18n.Msg{Key: "dev.alert.file.not_read"}, i18n.DevAlertSetasideDetail())
	if a.Keyed() || a.DetailKey != "" || !strings.HasPrefix(a.Detail, "the device stopped twice") {
		t.Errorf("a bad title message: %+v", a)
	}
	a = New(i18n.DevAlertMemoryRestarted(), i18n.Msg{Key: "dev.alert.no_such_key"})
	if a.Keyed() {
		t.Errorf("a bad detail message: %+v", a)
	}

	if a := English("t", "d"); a.Keyed() || a.Title != "t" || a.Detail != "d" {
		t.Errorf("English: %+v", a)
	}
}

// A details line in English, and its twin in msg_lines.
func TestLines(t *testing.T) {
	at := time.Date(2026, 10, 11, 14, 5, 0, 0, time.Local)
	a := WithRaw(i18n.DevAlertFileNotRead("x.jpg"), "EOF")
	if got := a.EnglishLine(at); got != "14:05 x.jpg could not be read: EOF" {
		t.Errorf("EnglishLine = %q", got)
	}
	if got := New(i18n.DevAlertMemoryRestarted(), i18n.Msg{}).EnglishLine(at); got != "14:05 Your device ran out of memory and restarted" {
		t.Errorf("EnglishLine without a detail = %q", got)
	}
	l := a.Line(at)
	if l.At != at.UTC().Format(time.RFC3339) || l.Key != a.Key || string(l.Args) != `{"name":"x.jpg"}` || l.Detail != "EOF" || l.DetailKey != "" {
		t.Errorf("Line = %+v", l)
	}
	k := New(i18n.DevAlertRaidWorkingTitle(), i18n.DevAlertRaidWorkingDetail())
	if d := k.DetailLine(); d.At != "" || d.Key != "" || d.Detail != "" || d.DetailKey != "dev.alert.raid.working_detail" {
		t.Errorf("DetailLine = %+v", d)
	}
	raw, ok := Lines(l)
	if !ok {
		t.Fatal("one line doesn't fit")
	}
	var back []Line
	if err := json.Unmarshal(raw, &back); err != nil || len(back) != 1 || back[0].Key != l.Key {
		t.Errorf("Lines = %s (%v)", raw, err)
	}
}

// msg_lines stops at MaxLines lines and MaxLinesBytes bytes, as details
// stop at their own cap; what isn't a list of lines is left alone.
func TestAppendLineCaps(t *testing.T) {
	l := WithRaw(i18n.DevAlertFileNotRead("x.jpg"), "EOF").Line(time.Now())
	stored, ok := Lines(l)
	if !ok {
		t.Fatal(ok)
	}
	for i := 1; i < MaxLines; i++ {
		if stored, ok = AppendLine(stored, l); !ok {
			t.Fatalf("line %d refused at %d bytes", i+1, len(stored))
		}
	}
	if _, ok := AppendLine(stored, l); ok {
		t.Error("a line past MaxLines")
	}
	big := WithRaw(i18n.DevAlertFileNotRead("x.jpg"), strings.Repeat("é", MaxLinesBytes/4)).Line(time.Now())
	stored, _ = Lines(big)
	if _, ok := AppendLine(stored, big); ok {
		t.Error("a line past MaxLinesBytes")
	}
	if _, ok := Lines(WithRaw(i18n.DevAlertFileNotRead("x"), strings.Repeat("a", MaxLinesBytes)).Line(time.Now())); ok {
		t.Error("a first line past MaxLinesBytes")
	}
	for _, bad := range []string{"", "null", "{}", "[", `"x"`} {
		if _, ok := AppendLine([]byte(bad), l); ok {
			t.Errorf("appended to %q", bad)
		}
	}
	if out, ok := AppendLine([]byte("[]"), l); !ok || !strings.HasPrefix(string(out), `[{"at":`) {
		t.Errorf("to an empty list: %s", out)
	}
}

// A request without a language, in English or in a language this build
// doesn't carry gets the stored English as it is, whatever the row holds.
func TestRenderEnglishIsTheStoredEnglish(t *testing.T) {
	a := WithRaw(i18n.DevAlertFileNotRead("x.jpg"), "EOF")
	lines, _ := Lines(a.Line(time.Now()))
	const title, details = "stored title", "stored details"
	for _, lang := range []string{"", "en", "EN-gb", "xx", "<b>"} {
		if !englishOnly(lang) {
			continue
		}
		if gt, gd := Render(lang, a.Key, a.Args, lines, title, details); gt != title || gd != details {
			t.Errorf("%q: %q / %q", lang, gt, gd)
		}
	}
	// No key: the row's English in every language.
	for _, l := range i18n.Languages() {
		if gt, gd := Render(l.Code, "", nil, nil, title, details); gt != title || gd != details {
			t.Errorf("no key, %s: %q / %q", l.Code, gt, gd)
		}
	}
}

// englishOnly is devtext.English, for the test's own choice of languages.
func englishOnly(lang string) bool {
	code := i18n.Normalize(lang)
	return code == "" || code == "en"
}

// otherLanguage is a language this build carries besides English - the
// pseudo-locale in a `make i18n DRAFT=1` build, a shipping translation
// later - or the test is skipped: an English-only build renders every
// alert as its stored English (TestRenderEnglishIsTheStoredEnglish).
func otherLanguage(t *testing.T) string {
	t.Helper()
	langs := i18n.Languages()
	for _, l := range langs {
		if l.Code == "qps" {
			return l.Code
		}
	}
	for _, l := range langs {
		if l.Code != "en" {
			return l.Code
		}
	}
	t.Skip("this build carries only English")
	return ""
}

// In another language a stored alert is rendered from its keys: the title,
// and each line of its details (time, title, detail) - the raw detail as
// it is. Anything that doesn't match the catalog shows the row's English
// whole.
func TestRenderInAnotherLanguage(t *testing.T) {
	lang := otherLanguage(t)
	at := time.Date(2026, 10, 11, 14, 5, 0, 0, time.Local)
	first := WithRaw(i18n.DevAlertFileNotRead("x.jpg"), "EOF")
	second := New(i18n.DevAlertIntegrityLost(3), i18n.DevAlertIntegrityDetail("/a; /b; /c"))
	lines, _ := Lines(first.Line(at), second.Line(at.Add(time.Minute)))
	const title, details = "stored title", "stored details"

	gt, gd := Render(lang, first.Key, first.Args, lines, title, details)
	wantTitle := i18n.DevAlertFileNotRead("x.jpg").Render(lang)
	wantDetails := i18n.DevAlertLineDetail("14:05", wantTitle, "EOF").Render(lang) + "\n" +
		i18n.DevAlertLineDetail("14:06", i18n.DevAlertIntegrityLost(3).Render(lang), i18n.DevAlertIntegrityDetail("/a; /b; /c").Render(lang)).Render(lang)
	if gt != wantTitle || gd != wantDetails {
		t.Errorf("%s:\n got %q\n     %q\nwant %q\n     %q", lang, gt, gd, wantTitle, wantDetails)
	}
	if gt == title || strings.Contains(gd, "could not be read") || !strings.Contains(gd, "EOF") {
		t.Errorf("%s: English left in %q / %q", lang, gt, gd)
	}

	// A row of a storage or update alert: its details are the detail alone.
	storage := New(i18n.DevAlertRaidStoppedTop(), i18n.DevAlertRaidStoppedDetailDisk("sdb"))
	detailOnly, _ := Lines(storage.DetailLine())
	if gt, gd := Render(lang, storage.Key, storage.Args, detailOnly, title, details); gt != i18n.DevAlertRaidStoppedTop().Render(lang) || gd != i18n.DevAlertRaidStoppedDetailDisk("sdb").Render(lang) {
		t.Errorf("detail alone: %q / %q", gt, gd)
	}
	// A line without a detail.
	plain, _ := Lines(New(i18n.DevAlertMemoryRestarted(), i18n.Msg{}).Line(at))
	if _, gd := Render(lang, "dev.alert.memory.restarted", []byte("{}"), plain, title, details); gd != i18n.DevAlertLinePlain("14:05", i18n.DevAlertMemoryRestarted().Render(lang)).Render(lang) {
		t.Errorf("a line without a detail: %q", gd)
	}

	for name, c := range map[string]struct {
		key         string
		args, lines string
	}{
		"an unknown key":                  {"dev.alert.from_a_newer_release", `{}`, string(lines)},
		"arguments missing":               {first.Key, `{}`, string(lines)},
		"an argument too many":            {first.Key, `{"name":"x","extra":1}`, string(lines)},
		"an argument of a type":           {"dev.alert.integrity.lost", `{"count":"three"}`, string(lines)},
		"arguments not JSON":              {first.Key, `{"name":`, string(lines)},
		"lines not JSON":                  {first.Key, string(first.Args), `[{"at":`},
		"a line's unknown key":            {first.Key, string(first.Args), `[{"at":"2026-10-11T12:05:00Z","key":"dev.alert.nope","args":{}}]`},
		"a line's bad arguments":          {first.Key, string(first.Args), `[{"at":"2026-10-11T12:05:00Z","key":"dev.alert.file.not_read","args":{"nom":"x"}}]`},
		"a line's bad detail key":         {first.Key, string(first.Args), `[{"at":"2026-10-11T12:05:00Z","key":"dev.alert.file.not_read","args":{"name":"x"},"detail_key":"dev.alert.nope"}]`},
		"a line's bad time":               {first.Key, string(first.Args), `[{"at":"yesterday","key":"dev.alert.file.not_read","args":{"name":"x"}}]`},
		"a line with no time":             {first.Key, string(first.Args), `[{"key":"dev.alert.file.not_read","args":{"name":"x"}}]`},
		"no lines but details":            {first.Key, string(first.Args), ``},
		"an empty list":                   {first.Key, string(first.Args), `[]`},
		"a msg argument that isn't a key": {"dev.alert.wifi.restart_failed", `{"speed":"1","pct":"2","reason":"dev.alert.nope"}`, `[]`},
	} {
		if gt, gd := Render(lang, c.key, []byte(c.args), []byte(c.lines), title, details); gt != title || gd != details {
			t.Errorf("%s: %q / %q, want the stored English whole", name, gt, gd)
		}
	}
}
