// SPDX-License-Identifier: AGPL-3.0-or-later

package devtext

import (
	"errors"
	"fmt"
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

func TestEnglish(t *testing.T) {
	for _, lang := range []string{"", "en", "EN", "en-GB", "en_US.UTF-8", "zz", "../x"} {
		if !English(lang) {
			t.Errorf("%q should render English", lang)
		}
	}
}

func TestErrorText(t *testing.T) {
	cause := errors.New("no space left on device")
	e := Wrap(i18n.DevUpdateRequestFailed(cause.Error()), cause)
	if e.Error() != "requesting the update: no space left on device" {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Error("errors.Is lost the cause")
	}
	for _, lang := range []string{"", "en", "fr"} {
		if got := Text(lang, e); got != e.Error() {
			t.Errorf("Text(%q) = %q", lang, got)
		}
	}

	// The English given is the English, byte for byte: no isolation marks
	// around the user argument the catalog text has.
	w := WithEnglish("Confirm your email first: we sent a link to a@b.c. Open it, then sign in here again to continue.",
		i18n.DevStatusBridgeEmailNotVerified("a@b.c"))
	if Text("", w) != w.english || Text("en", w) != w.english {
		t.Errorf("WithEnglish rendered %q", Text("", w))
	}

	// Only the error itself counts: a wrapper keeps its own words.
	wrapped := fmt.Errorf("leaving the bridge: %w", New(i18n.DevStatusBridgeNeedsUpdate()))
	if got := Text("en", wrapped); got != wrapped.Error() {
		t.Errorf("a wrapper's words were lost: %q", got)
	}
	plain := errors.New("plain")
	if Text("es", plain) != "plain" {
		t.Error("a plain error changed")
	}
}

// A runner's message renders as written in English, without a code, with
// a code the table doesn't know (a newer runner), and with arguments that
// don't fit the key.
func TestRunnerTextFallsBackToTheMessage(t *testing.T) {
	cases := []struct {
		code, args, message string
	}{
		{"downloading", `{"release":"118"}`, "Downloading release 118"},
		{"", "", "Downloading release 118"},
		{"downloading", "", "Downloading release 118"},
		{"downloading", `{"release":118}`, "Downloading release 118"},
		{"downloading", `{"version":"118"}`, "Downloading release 118"},
		{"never_heard_of", `{}`, "something new"},
		{"dev.update.downloading", `{"release":"1"}`, "a key is not a code"},
	}
	for _, c := range cases {
		for _, lang := range []string{"", "en", "pt"} {
			if got := UpdateStatus(lang, c.code, []byte(c.args), c.message); got != c.message {
				t.Errorf("%q %s in %q: %q", c.code, c.args, lang, got)
			}
		}
	}
	// One table's codes mean nothing to another reader.
	if got := BridgeStatus("de", "downloading", []byte(`{"release":"1"}`), "x"); got != "x" {
		t.Errorf("an update code read as a bridge one: %q", got)
	}
	if WifiRestartKey("too_soon") != "dev.status.wifi.too_soon" || WifiRestartKey("") != "" || WifiRestartKey("building") != "" {
		t.Error("WifiRestartKey")
	}
}

// Every code in the tables names a key the catalog has.
func TestRunnerCodesHaveKeys(t *testing.T) {
	for name, table := range map[string]map[string]string{
		"update": updateCodes, "bridge": bridgeCodes, "tailscale": tailscaleCodes, "wifi": wifiRestartCodes,
	} {
		for code, key := range table {
			if _, err := i18n.ParseStored(key, nil); errors.Is(err, i18n.ErrUnknownKey) {
				t.Errorf("%s code %q: no key %q in the catalog", name, code, key)
			}
		}
	}
}
