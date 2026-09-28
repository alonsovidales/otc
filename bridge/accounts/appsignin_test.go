// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestAppReturnNeedsAChallenge(t *testing.T) {
	sum := sha256.Sum256([]byte("verifier-verifier-verifier-verifier-verifier"))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if _, ok := appReturnWithChallenge(challenge); !ok {
		t.Fatal("a real S256 challenge was refused")
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 44), strings.Repeat("!", 43)} {
		if _, ok := appReturnWithChallenge(bad); ok {
			t.Errorf("challenge %q accepted", bad)
		}
	}
	if !verifierMatches("verifier-verifier-verifier-verifier-verifier", challenge) || verifierMatches("other", challenge) {
		t.Fatal("verifier check wrong")
	}
	for raw, want := range map[string]bool{
		"otcsetup://done":                        true,
		"otcsetup://done?challenge=" + challenge: true,
		"otcsetup://evil":                        false,
		"otcsetup://done?challenge=x":            false,
		"https://evil.example/":                  false,
		"http://192.168.1.10/":                   true,
	} {
		if _, ok := validReturnURL(raw); ok != want {
			t.Errorf("validReturnURL(%q) = %v, want %v", raw, ok, want)
		}
	}
}

// The redirect to the app carries only a one-time code, never the token.
func TestAppRedirectCarriesOnlyACode(t *testing.T) {
	sum := sha256.Sum256([]byte("v"))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	ret, _ := appReturnWithChallenge(challenge)
	a := &Accounts{}
	dest := a.appRedirect("acc-1", ret)
	u, err := url.Parse(dest)
	if err != nil || !isAppReturn(u) || u.Query().Get("code") == "" || u.Query().Get("setup_token") != "" {
		t.Fatalf("redirect %q", dest)
	}
	pendingAppCodes.mu.Lock()
	c := pendingAppCodes.codes[u.Query().Get("code")]
	pendingAppCodes.mu.Unlock()
	if c.accountID != "acc-1" || c.challenge != challenge {
		t.Fatalf("stored %+v", c)
	}
}
