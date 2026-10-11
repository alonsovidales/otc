// SPDX-License-Identifier: AGPL-3.0-or-later

package bridgeaccess

import (
	"errors"
	"os"
	"testing"

	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
)

// The device's own refusals keep their English. Claim checks the name and
// the token before it calls the bridge, so nothing here reaches a network.
func TestRefusalsEnglishUnchanged(t *testing.T) {
	if _, err := Claim("Not A Name!", "token"); err == nil || err.Error() != "lower-case letters, digits and hyphens only" {
		t.Errorf("bad name: %v", err)
	}
	if _, err := Claim("good-name", ""); err == nil || err.Error() != "sign in to your Off The Cloud account first" {
		t.Errorf("no token: %v", err)
	}
	if _, err := os.Stat(cRootRunner); err != nil {
		if err := CanSwitchOn(); err == nil || err.Error() != "this device needs updating before it can join the bridge: Settings → Update" {
			t.Errorf("no runner: %v", err)
		}
	}
	cause := errors.New("permission denied")
	cases := []struct {
		err  error
		want string
	}{
		{devtext.New(i18n.DevStatusBridgeInvalidSetupCode()), "that setup code is not valid or has expired"},
		{devtext.Wrap(i18n.DevStatusBridgeSwitchOnFailed(cause.Error()), cause), "switching the bridge on: permission denied"},
		{devtext.Wrap(i18n.DevStatusBridgeSwitchOffFailed(cause.Error()), cause), "switching the bridge off: permission denied"},
		{devtext.Wrap(i18n.DevStatusBridgeUnreachable("off-the.cloud", cause.Error()), cause), "could not reach off-the.cloud: permission denied"},
	}
	for _, c := range cases {
		if c.err.Error() != c.want || devtext.Text("", c.err) != c.want {
			t.Errorf("got %q, want %q", c.err.Error(), c.want)
		}
	}
}

// A refusal from the bridge reaches the apps in the bridge's own English,
// as it always did, whatever the code; with no text at all, the device's
// own fallback.
func TestBridgeRefusalEnglishUnchanged(t *testing.T) {
	// The bridge's texts for the codes mapped (bridge/accounts, bridge/api).
	bridgeTexts := map[string]string{
		"too_many_attempts":   "too many failed attempts, try again later",
		"signin_unavailable":  "could not sign in right now",
		"invalid_credentials": "wrong email or password - if you signed up with Google or Apple, use that instead (on the account page, then a setup code)",
		"email_not_verified":  "Confirm your email first: we sent a link to ana@example.com. Open it, then sign in here again to continue.",
		"terms_not_accepted":  "accept the terms of use first: sign in at off-the.cloud/account and accept them there",
		"setup_unavailable":   "could not start the setup",
		"claim_unavailable":   "could not reserve that name right now",
		"login_required":      "sign in to register a name",
		"invalid_name":        "that name can't be used",
		"too_many_claims":     "please wait a moment before trying another name",
		"name_taken":          "that name is already taken",
		"name_held":           "that name was released recently and is held for its previous owner for 30 days",
		"domain_limit":        "an account can hold 3 names",
		"":                    "something the bridge said",
	}
	for code, english := range bridgeTexts {
		for _, codes := range []map[string]func(string) (i18n.Msg, bool){signInCodes, claimCodes} {
			err := bridgeRefusal(codes, code, english, "ana@example.com", i18n.DevStatusBridgeSigninFailed())
			for _, lang := range []string{"", "en", "ja"} { // "ja": a language no build carries
				if got := devtext.Text(lang, err); got != english {
					t.Errorf("%s in %q: %q", code, lang, got)
				}
			}
		}
		// And the device's own text for each mapped code is the bridge's
		// English today (the email one with its address).
		if text, ok := signInCodes[code]; ok {
			m, _ := text("ana@example.com")
			if got := stripIsolation(m.Render("en")); got != english {
				t.Errorf("%s: the device's text %q is not the bridge's %q", code, got, english)
			}
		}
		if text, ok := claimCodes[code]; ok {
			m, _ := text("")
			if got := m.Render("en"); got != english {
				t.Errorf("%s: the device's text %q is not the bridge's %q", code, got, english)
			}
		}
	}
	if got := bridgeRefusal(signInCodes, "", "", "", i18n.DevStatusBridgeSigninFailed()).Error(); got != "could not sign in" {
		t.Errorf("sign-in fallback: %q", got)
	}
	if got := bridgeRefusal(claimCodes, "", "", "", i18n.DevStatusBridgeClaimFailed()).Error(); got != "could not reserve that name" {
		t.Errorf("claim fallback: %q", got)
	}
}

// stripIsolation drops the FSI/PDI marks the catalog puts around a user
// argument.
func stripIsolation(s string) string {
	out := []rune{}
	for _, r := range s {
		if r != '⁨' && r != '⁩' {
			out = append(out, r)
		}
	}
	return string(out)
}
