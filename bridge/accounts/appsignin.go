// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Issue #137 follow-up: "Continue with Apple/Google" inside the apps'
// Bluetooth setup. The app runs the provider sign-in in the system's
// sign-in sheet with return=otcsetup://done - its own address - and gets a
// setup token for the wizard, so nobody copies a setup code from another
// device any more.
//
// A custom scheme can be claimed by any app on Android, so the setup token
// never travels in that redirect. PKCE, as OAuth does for native apps: the
// app starts with a challenge (base64url SHA-256 of a secret verifier it
// keeps), the redirect carries only a one-time exchange code, and
// POST /api/account/app-exchange trades code + verifier for the token. An
// app that intercepts the redirect has the code but not the verifier.

// AppReturn is the apps' sign-in return address.
const AppReturn = "otcsetup://done"

const cAppCodeTTL = 5 * time.Minute

var challengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// codeHash is what is stored of a code: a leaked table can't be replayed.
func codeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// isAppReturn is whether a return URL is the apps' own address.
func isAppReturn(u *url.URL) bool {
	return u.Scheme == "otcsetup" && u.Host == "done"
}

// appReturnWithChallenge is the return URL stored with the OAuth state:
// the app's address carrying its challenge, which must be a valid one.
func appReturnWithChallenge(challenge string) (string, bool) {
	if !challengePattern.MatchString(challenge) {
		return "", false
	}
	return AppReturn + "?challenge=" + challenge, true
}

// appRedirect finishes a sign-in that started in an app: a one-time code
// for app-exchange, never the setup token itself.
func (a *Accounts) appRedirect(accountID, returnURL string) string {
	u, err := url.Parse(returnURL)
	if err != nil {
		return "/account"
	}
	challenge := u.Query().Get("challenge")
	if !challengePattern.MatchString(challenge) {
		return "/account"
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "/account"
	}
	code := hex.EncodeToString(buf)
	// Issue #144: in the database - the exchange can reach another node.
	if err := a.dao.SaveAppCode(codeHash(code), accountID, challenge, time.Now(), cAppCodeTTL); err != nil {
		return "/account"
	}

	return AppReturn + "?code=" + code
}

// verifierMatches is PKCE's S256 check.
func verifierMatches(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// AppExchange trades {code, verifier} for a setup token.
// POST /api/account/app-exchange.
func (a *Accounts) AppExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string `json:"code"`
		Verifier string `json:"verifier"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Single use, right or wrong.
	accountID, challenge, created, ok, err := a.dao.ConsumeAppCode(codeHash(strings.TrimSpace(body.Code)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not finish the sign-in")
		return
	}
	if !ok || time.Since(created) > cAppCodeTTL || !verifierMatches(body.Verifier, challenge) {
		writeError(w, http.StatusUnauthorized, "that sign-in has expired - try again")
		return
	}
	tok, err := a.IssueSetupToken(accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not finish the sign-in")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"setup_token": tok})
}
