// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"
	"time"
)

// A config.json from before localization is Automatic with nothing to
// send, and saves without the new keys; a choice survives a save and a
// load, pending mark and expected value included.
func TestLanguageCompatibleAndKept(t *testing.T) {
	withConfigDir(t)
	writeConfigJSON(t, `{"domain": "cala.off-the.cloud", "client_id": "abc", "folders": [], "remote_folders": []}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "" || cfg.LanguagePending != nil || cfg.LanguageExpected != nil {
		t.Fatalf("a choice out of nowhere: %+v", cfg)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if raw := readConfigJSON(t); strings.Contains(raw, "language") {
		t.Fatalf("saved with the new keys:\n%s", raw)
	}

	at := time.Date(2026, 10, 11, 9, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	automatic := ""
	cfg.SetLanguage("es", &automatic, at)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.Language != "es" || back.LanguagePending == nil || !back.LanguagePending.Equal(at) ||
		back.LanguageExpected == nil || *back.LanguageExpected != "" {
		t.Fatalf("not kept: %+v", back)
	}
	if raw := readConfigJSON(t); !strings.Contains(raw, `"language_expected": ""`) {
		t.Fatalf("an expected Automatic must be written, not left out:\n%s", raw)
	}

	// Disconnect (`otc-sync disconnect`, the tray) clears the device and
	// the folders; the choice is the user's and stays.
	back.Folders, back.RemoteFolders, back.Domain = nil, nil, ""
	if err := back.Save(); err != nil {
		t.Fatal(err)
	}
	if again, _ := Load(); again.Language != "es" {
		t.Fatalf("the choice went with the device: %+v", again)
	}
}

// Compare-and-clear: an answer clears only the change it was about.
func TestLanguagePendingCompareAndClear(t *testing.T) {
	now := time.Now()
	cfg := &Config{}
	cfg.SetLanguage("es", nil, now)
	if cfg.LanguageExpected != nil {
		t.Fatal("no device value seen: nothing expected")
	}
	if cfg.ClearLanguagePending("fr") {
		t.Fatal("cleared by the answer to another value")
	}
	// A change made while "es" was on its way: it counts against "es" now.
	cfg.SetLanguage("fr", nil, now.Add(time.Second))
	if cfg.ClearLanguagePending("es") || !cfg.SetLanguageExpected("es") {
		t.Fatal("the newer change must stay pending, against es")
	}
	if cfg.LanguageExpected == nil || *cfg.LanguageExpected != "es" {
		t.Fatalf("expected %v", cfg.LanguageExpected)
	}
	if !cfg.ClearLanguagePending("fr") || cfg.LanguagePending != nil || cfg.LanguageExpected != nil {
		t.Fatalf("not cleared: %+v", cfg)
	}
	if cfg.ClearLanguagePending("fr") || cfg.SetLanguageExpected("fr") {
		t.Fatal("nothing pending any more")
	}
}

// Adopting the device's value only replaces what the engine looked at.
func TestAdoptLanguage(t *testing.T) {
	now := time.Now()
	cfg := &Config{Language: "es"}
	if cfg.AdoptLanguage("fr", "de", nil) || cfg.Language != "es" {
		t.Fatal("adopted over a choice the engine didn't see")
	}
	if !cfg.AdoptLanguage("fr", "es", nil) || cfg.Language != "fr" {
		t.Fatalf("not adopted: %+v", cfg)
	}
	cfg.SetLanguage("de", nil, now)
	if cfg.AdoptLanguage("es", "de", nil) {
		t.Fatal("adopted over a pending change it didn't see")
	}
	seen := now.Add(0)
	if !cfg.AdoptLanguage("", "de", &seen) || cfg.Language != "" || cfg.LanguagePending != nil {
		t.Fatalf("a pending change that timed out: %+v", cfg)
	}
}

// A device set up but none of its values seen (the engine never signed in
// to it - a wrong password -, a device that predates localization, no sync
// running): a change is for the next device too, so Disconnect, the only
// way to fix the password, keeps it (engine's
// TestLanguageChosenBeforeSigningInSurvivesDisconnect). A value seen makes
// it this device's.
func TestLanguageBeforeAnyValueOfTheDevice(t *testing.T) {
	now := time.Now()
	cfg := &Config{Domain: "cala.off-the.cloud"}
	cfg.SetLanguage("es", nil, now)
	if !cfg.LanguageNextDevice || cfg.LanguageExpected != nil {
		t.Fatalf("nothing seen: %+v", cfg)
	}
	seen := "it"
	cfg = &Config{Domain: "cala.off-the.cloud"}
	cfg.SetLanguage("es", &seen, now)
	if cfg.LanguageNextDevice || cfg.LanguageExpected == nil || *cfg.LanguageExpected != "it" {
		t.Fatalf("its value seen: %+v", cfg)
	}
}

// A change made with no device set up is for the next one: against
// nothing (a value a stale state names doesn't count), still so when made
// again before signing in to the device set up since, and that device's
// once signed in to - after which a change counts against what was seen.
func TestLanguageForTheNextDevice(t *testing.T) {
	withConfigDir(t)
	now := time.Now()
	seen := "fr"
	cfg := &Config{}
	cfg.SetLanguage("es", &seen, now)
	if !cfg.LanguageNextDevice || cfg.LanguageExpected != nil {
		t.Fatalf("no device: %+v", cfg)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if raw := readConfigJSON(t); !strings.Contains(raw, `"language_next_device": true`) {
		t.Fatalf("not saved:\n%s", raw)
	}
	back, err := Load()
	if err != nil || !back.LanguageNextDevice {
		t.Fatalf("not kept: %+v %v", back, err)
	}

	cfg.Domain = "cala.off-the.cloud"
	cfg.SetLanguage("de", &seen, now)
	if !cfg.LanguageNextDevice || cfg.LanguageExpected != nil {
		t.Fatalf("before signing in: %+v", cfg)
	}
	if !cfg.SignedInForLanguage() || cfg.LanguageNextDevice || cfg.LanguagePending == nil {
		t.Fatalf("signed in: %+v", cfg)
	}
	if cfg.SignedInForLanguage() {
		t.Fatal("signed in twice")
	}
	cfg.SetLanguage("it", &seen, now)
	if cfg.LanguageNextDevice || cfg.LanguageExpected == nil || *cfg.LanguageExpected != "fr" {
		t.Fatalf("signed in, then chosen: %+v", cfg)
	}

	// Answered, adopted or dropped: nothing is left for the next device.
	for _, end := range []func(c *Config){
		func(c *Config) { c.ClearLanguagePending(c.Language) },
		func(c *Config) { c.AdoptLanguage("pt", c.Language, c.LanguagePending) },
		func(c *Config) { c.DropLanguagePending(c.LanguagePending) },
	} {
		c := &Config{}
		c.SetLanguage("nl", nil, now)
		end(c)
		if c.LanguagePending != nil || c.LanguageNextDevice {
			t.Fatalf("left: %+v", c)
		}
	}
}
