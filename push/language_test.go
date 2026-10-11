// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import "testing"

// The push language: the user's choice, else the language of the app that
// last registered for pushes, else English.
func TestResolveLanguage(t *testing.T) {
	for _, c := range []struct{ stored, lastUI, want string }{
		{"", "", "en"},
		{"", "es", "es"},
		{"fr", "es", "fr"},
		{"fr", "", "fr"},
		{"ja", "es", "ja"}, // a choice this build has no text for still wins
	} {
		if got := ResolveLanguage(c.stored, c.lastUI); got != c.want {
			t.Errorf("ResolveLanguage(%q, %q) = %q, want %q", c.stored, c.lastUI, got, c.want)
		}
	}
}

// Lang is English until the device wires its settings in (and always on
// the bridge, which never does).
func TestLang(t *testing.T) {
	p := &Push{}
	if got := p.Lang(); got != "en" {
		t.Errorf("unset: %q", got)
	}
	p.Language = func() string { return "" }
	if got := p.Lang(); got != "en" {
		t.Errorf("empty: %q", got)
	}
	p.Language = func() string { return "es" }
	if got := p.Lang(); got != "es" {
		t.Errorf("wired: %q", got)
	}
}
