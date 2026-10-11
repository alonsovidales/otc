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
