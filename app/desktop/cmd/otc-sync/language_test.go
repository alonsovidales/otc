// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
)

func languageCmd(t *testing.T, now time.Time, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runLanguage(args, &out, now)

	return out.String(), err
}

// Without an argument: the choice and what it gives; `auto` and a code
// record a pending change against the device's value the running sync
// last saw; a code this version doesn't have is refused.
func TestLanguageCommand(t *testing.T) {
	withFakes(t, &fakeLoginItems{})
	now := time.Now()

	out, err := languageCmd(t, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "language:  Automatic ("+oslang.Name(oslang.System())+")") ||
		!strings.Contains(out, "device:    unknown (the sync is not running)") || strings.Contains(out, "pending:") {
		t.Fatalf("a fresh install:\n%s", out)
	}

	// A running sync that has seen the device's value.
	device := "fr"
	if err := config.SaveState(&config.State{Status: "Connected", DeviceLanguage: &device}); err != nil {
		t.Fatal(err)
	}
	out, err = languageCmd(t, now, "en")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "en" || cfg.LanguagePending == nil || cfg.LanguageExpected == nil || *cfg.LanguageExpected != "fr" {
		t.Fatalf("config.json: %+v", cfg)
	}
	if !strings.Contains(out, "language:  English (en)") || !strings.Contains(out, "shows:     English (en)") ||
		!strings.Contains(out, "device:    Français (fr)") && !strings.Contains(out, "device:    fr (fr)") ||
		!strings.Contains(out, "pending:") {
		t.Fatalf("after choosing English:\n%s", out)
	}

	// The same again changes nothing: the first change stays the one sent.
	if _, err := languageCmd(t, now.Add(time.Minute), "en"); err != nil {
		t.Fatal(err)
	}
	if again, _ := config.Load(); !again.LanguagePending.Equal(*cfg.LanguagePending) {
		t.Fatalf("recorded again: %v", again.LanguagePending)
	}

	if _, err := languageCmd(t, now, "auto"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = config.Load(); cfg.Language != "" || cfg.LanguagePending == nil {
		t.Fatalf("auto: %+v", cfg)
	}

	if _, err := languageCmd(t, now, "xx"); err == nil || !strings.Contains(err.Error(), "en") {
		t.Fatalf("an unknown code: %v", err)
	}
	if _, err := languageCmd(t, now, "en", "es"); err == nil {
		t.Fatal("two arguments")
	}
}

// What the device says, in words: an old device, Automatic, a language.
func TestLanguageReportDevice(t *testing.T) {
	cfg := &config.Config{Language: "sv"}
	auto := ""
	for _, c := range []struct {
		st   *config.State
		want string
	}{
		{&config.State{LanguageUnsupported: true}, "can't store a language"},
		{&config.State{}, "not connected yet"},
		{&config.State{DeviceLanguage: &auto}, "device:    Automatic"},
	} {
		out := languageReport(cfg, c.st)
		if !strings.Contains(out, c.want) {
			t.Errorf("want %q in:\n%s", c.want, out)
		}
		if !strings.Contains(out, "sv (sv) - not in this version, shown in English") || !strings.Contains(out, "shows:     English (en)") {
			t.Errorf("a language this version doesn't have:\n%s", out)
		}
	}
}
