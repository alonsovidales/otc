// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP is a provider's token endpoint and JWKS: the id_token it hands
// out carries claims, signed with its own key.
type fakeIdP struct {
	claims jwt.MapClaims
}

func newFakeIdP(t *testing.T, a *Accounts) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{claims: jwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": "client-id", "sub": "google-sub",
		"email": "a@b.c", "email_verified": true, "exp": time.Now().Add(5 * time.Minute).Unix(),
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims)
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": s})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if a.providers == nil {
		a.providers = map[string]*provider{}
	}
	a.providers["google"] = &provider{
		name: "google", authURL: "https://accounts.google.test/auth", tokenURL: srv.URL + "/token", jwksURL: srv.URL + "/jwks",
		issuers: []string{"https://accounts.google.com"}, scope: "openid email profile", clientID: "client-id", clientSecret: "secret",
	}
	return f
}

// callbackRequest is the provider sending the browser back with state and
// code.
func callbackRequest(state, code string) *http.Request {
	r := httptest.NewRequest("GET", "/account/auth/google/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	r.SetPathValue("provider", "google")
	return r
}

// expectStateConsumed answers ConsumeOAuthState with a live state.
func expectStateConsumed(mock sqlmock.Sqlmock, state, returnURL string) {
	mock.ExpectBegin()
	mock.ExpectQuery("select `return_url`, `created` from `oauth_states`").WithArgs(state).
		WillReturnRows(sqlmock.NewRows([]string{"return_url", "created"}).AddRow(returnURL, time.Now()))
	mock.ExpectExec("delete from `oauth_states`").WithArgs(state).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

var errDBDown = errors.New("db down")

var accountCols = []string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at"}

// Someone signed up with the victim's email and kept the session; the
// victim then signs in with Google, which links and verifies that account.
// The squatter's session must end there (Reset does the same).
func TestProviderLinkEndsTheSquattersSessions(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	state := strings.Repeat("ab", 24)
	expectStateConsumed(mock, state, "")
	mock.ExpectQuery("from `accounts` where `id` = \\(select `account_id` from `account_logins`").WillReturnRows(sqlmock.NewRows(accountCols))
	mock.ExpectQuery("from `accounts` where `email` = \\?").WithArgs("a@b.c").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", "ES", "$2a$12$squatter", time.Now(), time.Now(), time.Now(), false, TermsVersion, time.Now()))
	mock.ExpectExec("update `accounts` set `password_hash`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("insert ignore into `account_logins`").WithArgs("google", "google-sub", "acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `accounts` set `email_verified` = 1").WithArgs("acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `accounts` set `session_epoch` = `session_epoch` \\+ 1").WithArgs("acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	epochRow(mock, 1) // read back by the bump
	epochRow(mock, 1) // the provider user's new cookie
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))

	w := httptest.NewRecorder()
	a.OAuthCallback(w, callbackRequest(state, "code"))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/account" {
		t.Fatalf("callback: %d %q", w.Code, w.Header().Get("Location"))
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), cSessionCookie+"=acc1|1|") {
		t.Errorf("the provider user's cookie is not on the new epoch: %q", w.Header().Get("Set-Cookie"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// When the old password can't be cleared, the account is not linked: the
// password would go on working on it.
func TestProviderLinkStopsWhenThePasswordStays(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	state := strings.Repeat("cd", 24)
	expectStateConsumed(mock, state, "")
	mock.ExpectQuery("from `accounts` where `id` = \\(select `account_id` from `account_logins`").WillReturnRows(sqlmock.NewRows(accountCols))
	mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", "ES", "$2a$12$squatter", time.Now(), time.Now(), time.Now(), false, TermsVersion, time.Now()))
	mock.ExpectExec("update `accounts` set `password_hash`").WillReturnError(errDBDown)

	w := httptest.NewRecorder()
	a.OAuthCallback(w, callbackRequest(state, "code"))
	if w.Code != http.StatusInternalServerError || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("a link that kept the password: %d, cookie %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// /api/account/continue hands out a setup token (or an app code), so only
// the account page's own navigation may reach it - not another site's link.
func TestContinueSetupOnlyFromTheAccountPage(t *testing.T) {
	sum := sha256.Sum256([]byte("verifier-verifier-verifier-verifier-verifier"))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	continueReq := func(ret string, headers map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/api/account/continue?return="+url.QueryEscape(ret), nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}

	// Allowed: the token goes to the wizard, the app gets a code.
	for _, h := range []map[string]string{
		{"Sec-Fetch-Site": "same-origin"},
		{"Referer": "https://off-the.cloud/account?complete=1"},
	} {
		a, mock := testAccounts(t)
		a.tld = "off-the.cloud"
		verifiedRow(mock, "acc1", true)
		mock.ExpectExec("insert into `account_tokens`").WillReturnResult(sqlmock.NewResult(1, 1))
		w := httptest.NewRecorder()
		a.ContinueSetup(w, continueReq("http://10.0.0.5/", h), "acc1")
		if loc := w.Header().Get("Location"); w.Code != http.StatusFound || !strings.HasPrefix(loc, "http://10.0.0.5/?setup_token=") {
			t.Errorf("%v: %d %q, want a redirect with a setup token", h, w.Code, loc)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	}
	a, mock := testAccounts(t)
	a.tld = "off-the.cloud"
	mock.ExpectExec("delete from `app_signin_codes`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("insert into `app_signin_codes`").WillReturnResult(sqlmock.NewResult(1, 1))
	w := httptest.NewRecorder()
	a.ContinueSetup(w, continueReq(AppReturn+"?challenge="+challenge, map[string]string{"Sec-Fetch-Site": "same-origin"}), "acc1")
	if loc := w.Header().Get("Location"); w.Code != http.StatusFound || !strings.HasPrefix(loc, AppReturn+"?code=") {
		t.Errorf("app return: %d %q", w.Code, loc)
	}

	// Refused, with nothing issued or stored (no database call at all).
	for _, h := range []map[string]string{
		{"Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Site": "same-site"}, // a device's page on a subdomain
		{"Sec-Fetch-Site": "none"},      // typed or from a bookmark
		{"Sec-Fetch-Site": "cross-site", "Referer": "https://off-the.cloud/account"},
		{"Referer": "https://evil.example/"},
		{"Referer": "http://off-the.cloud/account"},
		{},
	} {
		for _, ret := range []string{"http://10.0.0.5/", AppReturn + "?challenge=" + challenge} {
			a, mock := testAccounts(t)
			a.tld = "off-the.cloud"
			w := httptest.NewRecorder()
			a.ContinueSetup(w, continueReq(ret, h), "acc1")
			if w.Code != http.StatusForbidden {
				t.Errorf("%v to %s: %d %q, want 403", h, ret, w.Code, w.Header().Get("Location"))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		}
	}
}
