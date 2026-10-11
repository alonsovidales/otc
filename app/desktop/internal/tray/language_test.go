// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	"github.com/alonsovidales/otc/i18n"
)

// While English is the only language that ships the menu has no Language
// submenu at all (a DRAFT build has one).
func TestLanguageMenuOnlyWithAChoice(t *testing.T) {
	if got := oslang.Choosable(); got != (len(i18n.Languages()) > 1) {
		t.Fatalf("Choosable %v with %d languages", got, len(i18n.Languages()))
	}
}

// The check: Automatic, a language this build has, and one it doesn't -
// kept as the choice, shown as English.
func TestLanguageShownChoice(t *testing.T) {
	for choice, want := range map[string]string{"": "", "en": "en", "sv": "en"} {
		if got := shownChoice(choice); got != want {
			t.Errorf("shownChoice(%q) = %q, want %q", choice, got, want)
		}
	}
	if oslang.Carried("es") && shownChoice("es") != "es" {
		t.Error("a language this build has is checked as itself")
	}
}

// Its own text comes from the catalog in the language the choice gives;
// Automatic names the computer's language by its own name.
func TestLanguageMenuText(t *testing.T) {
	if got := languageTitle("en"); got != "Language" {
		t.Errorf("title %q", got)
	}
	got := automaticTitle("en")
	if !strings.HasPrefix(got, "Automatic (") || !strings.Contains(got, oslang.Name(oslang.System())) {
		t.Errorf("Automatic: %q", got)
	}
}

// A pick is the local copy, pending, against the device's value as the
// engine last saw it; with no engine running, against nothing.
func TestPickLanguage(t *testing.T) {
	now := time.Now()
	cfg := &config.Config{Language: "fr"}
	device := ""
	pickLanguage(cfg, "es", config.State{Status: "Connected", DeviceLanguage: &device}, now)
	if cfg.Language != "es" || cfg.LanguagePending == nil || !cfg.LanguagePending.Equal(now) ||
		cfg.LanguageExpected == nil || *cfg.LanguageExpected != "" {
		t.Fatalf("%+v", cfg)
	}
	pickLanguage(cfg, "", config.State{Status: "Sync not running"}, now)
	if cfg.Language != "" || cfg.LanguagePending == nil || cfg.LanguageExpected != nil {
		t.Fatalf("%+v", cfg)
	}
}
