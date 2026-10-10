// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"slices"
	"testing"
)

func TestHasLetters(t *testing.T) {
	for s, want := range map[string]bool{
		"OK": true, "of": true, "x": false, "•": false, "{}": false, "%d%%": false, "{} · {}": false,
		"%1$@ deleted": true, "12:30": false, "Ñu": true,
	} {
		if got := hasLetters(s); got != want {
			t.Errorf("hasLetters(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestIdentLike(t *testing.T) {
	for s, want := range map[string]bool{
		"photos": true, "otc-setup": true, "inode/directory": true, "faceRecognitionEnabled": true, "OTC_KEY": true,
		"photo.on.rectangle": true, "https://off-the.cloud": true, "#fff": true, "a_b": true, "": true,
		"Delete": false, "OK": false, "Done.": false, "Wait…": false, "Two words": false, "ISO": false,
	} {
		if got := identLike(s); got != want {
			t.Errorf("identLike(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestProse(t *testing.T) {
	cases := []struct {
		s             string
		loose, strict bool
	}{
		{"could not be read", true, true},
		{"not found", true, false},
		{"Try again", true, true},
		{"Saved.", false, false},
		{"msg bad", true, false},
		{"flex items-center gap-2", false, false},
		{"could not start the sign-in", true, true},
		{"0 0 4px rgba(0,0,0,.2)", false, false},
		{"SELECT name FROM files WHERE hash = ?", false, false},
		{"dd-MM-yyyy HH:mm", false, false},
		{"a && b", false, false},
		{"%s: %w", false, false},
		{"could not reach %s: %w", true, true},
		{"see /var/log/otc for it", false, false},
		{"Add sums", true, true},
	}
	for _, c := range cases {
		if got := isProse(c.s, loose); got != c.loose {
			t.Errorf("isProse(%q, loose) = %v, want %v", c.s, got, c.loose)
		}
		if got := isProse(c.s, strict); got != c.strict {
			t.Errorf("isProse(%q, strict) = %v, want %v", c.s, got, c.strict)
		}
	}
}

func TestNeverOnly(t *testing.T) {
	n := newNeverTerms([]string{"Off The Cloud", "Tailscale", "Tailscale Funnel", "Mac", "off-the.cloud"})
	for s, want := range map[string]bool{
		"Off The Cloud": true, "Tailscale Funnel": true, "Off The Cloud — Tailscale": true, "off-the.cloud": true,
		"Off The Cloud — Sync": false, "Machine": false, "a Mac app": false, "Mac": true,
	} {
		if got := n.only(s); got != want {
			t.Errorf("only(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestLooseAndRuns(t *testing.T) {
	s := &scanner{never: newNeverTerms([]string{"Off The Cloud"})}
	for text, want := range map[string]bool{
		"Delete": true, "OK": true, "delete": false, "otc_menu_open": false, "Off The Cloud": false,
		"e.g. casa": true, "%d photos": true, "{}": false,
	} {
		if got := s.looseText(text); got != want {
			t.Errorf("looseText(%q) = %v, want %v", text, got, want)
		}
	}
	for text, want := range map[string]bool{
		"or": true, "Read the policy": true, "DEVICE_UUID={} BRIDGE_SECRET={}": false,
		"http://otc.local:8080": false, "· {} ·": false,
	} {
		if got := s.textRun(text); got != want {
			t.Errorf("textRun(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestIgnoreMarker(t *testing.T) {
	for line, want := range map[string]bool{
		`Text("Hi") // i18n-ignore: a name`:                 true,
		`<p>Hi</p> <!-- i18n-ignore: product name -->`:      true,
		`<span>Hi</span> {/* i18n-ignore: product name */}`: true,
		`"error": "x"  # i18n-ignore: protocol value`:       true,
		`Text("Hi") // i18n-ignore:`:                        false,
		`<p>Hi</p> <!-- i18n-ignore: -->`:                   false,
		`Text("i18n-ignore: not a comment")`:                false,
	} {
		if got := hasIgnore(line); got != want {
			t.Errorf("hasIgnore(%q) = %v, want %v", line, got, want)
		}
	}
	gen := generatedLines([]string{"a", "# BEGIN GENERATED I18N", "b", "# END GENERATED I18N", "c"})
	if !slices.Equal(gen, []bool{false, true, true, true, false}) {
		t.Errorf("generated lines %v", gen)
	}
}
