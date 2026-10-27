// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/limits"
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
// code, without the cookie OAuthStart set (withStateCookie adds it).
func callbackRequest(state, code string) *http.Request {
	r := httptest.NewRequest("GET", "/account/auth/google/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	r.SetPathValue("provider", "google")
	return r
}

// withStateCookie adds the cookie OAuthStart set for state: the
// SameSite=None one, or the legacy one Safari 12 keeps.
func withStateCookie(r *http.Request, state string, legacy bool) *http.Request {
	name, legacyName := oauthCookieNames(state)
	if legacy {
		name = legacyName
	}
	r.AddCookie(&http.Cookie{Name: name, Value: state})
	return r
}

// sessionCookie is the account session cookie a response sets, if any.
func sessionCookie(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == cSessionCookie {
			return c.Value
		}
	}
	return ""
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

var accountCols = []string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at", "lang"}

// Someone signed up with the victim's email and kept the session; the
// victim then signs in with Google, which links and verifies that account.
// The squatter's session must end there (Reset does the same).
func TestProviderLinkEndsTheSquattersSessions(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	state := strings.Repeat("ab", 32)
	expectStateConsumed(mock, state, "")
	mock.ExpectQuery("from `accounts` where `id` = \\(select `account_id` from `account_logins`").WillReturnRows(sqlmock.NewRows(accountCols))
	mock.ExpectQuery("from `accounts` where `email` = \\?").WithArgs("a@b.c").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", "ES", "$2a$12$squatter", time.Now(), time.Now(), time.Now(), false, TermsVersion, time.Now(), ""))
	mock.ExpectExec("update `accounts` set `password_hash`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("insert ignore into `account_logins`").WithArgs("google", "google-sub", "acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `accounts` set `email_verified` = 1").WithArgs("acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `accounts` set `session_epoch` = `session_epoch` \\+ 1").WithArgs("acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	epochRow(mock, 1) // read back by the bump
	epochRow(mock, 1) // the provider user's new cookie
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))

	w := httptest.NewRecorder()
	a.OAuthCallback(w, withStateCookie(callbackRequest(state, "code"), state, false))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/account" {
		t.Fatalf("callback: %d %q", w.Code, w.Header().Get("Location"))
	}
	if c := sessionCookie(w); !strings.HasPrefix(c, "acc1|1|") {
		t.Errorf("the provider user's cookie is not on the new epoch: %q", c)
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
	state := strings.Repeat("cd", 32)
	expectStateConsumed(mock, state, "")
	mock.ExpectQuery("from `accounts` where `id` = \\(select `account_id` from `account_logins`").WillReturnRows(sqlmock.NewRows(accountCols))
	mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", "ES", "$2a$12$squatter", time.Now(), time.Now(), time.Now(), false, TermsVersion, time.Now(), ""))
	mock.ExpectExec("update `accounts` set `password_hash`").WillReturnError(errDBDown)

	w := httptest.NewRecorder()
	a.OAuthCallback(w, withStateCookie(callbackRequest(state, "code"), state, false))
	if w.Code != http.StatusInternalServerError || sessionCookie(w) != "" {
		t.Fatalf("a link that kept the password: %d, cookie %q", w.Code, sessionCookie(w))
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

// The state is tied to the browser that started the sign-in: a callback
// link someone else started (a login CSRF into their account) is refused
// before the state is even consumed.
func TestOAuthStateBelongsToTheBrowser(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)

	// Start: the state is in the database and in both cookies.
	var state string
	mock.ExpectExec("insert into `oauth_states`").WithArgs(stateCapture{&state}, "", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	start := httptest.NewRequest("GET", "/account/auth/google/start", nil)
	start.SetPathValue("provider", "google")
	w := httptest.NewRecorder()
	a.OAuthStart(w, start)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "state="+state) {
		t.Fatalf("start: %d %q", w.Code, w.Header().Get("Location"))
	}
	name, legacy := oauthCookieNames(state)
	cookies := map[string]string{}
	for _, h := range w.Header().Values("Set-Cookie") {
		cookies[strings.SplitN(h, "=", 2)[0]] = h
	}
	for n, sameSite := range map[string]string{name: "; SameSite=None", legacy: ""} {
		h := cookies[n]
		for _, attr := range []string{n + "=" + state + ";", "; Path=/", "; Max-Age=900", "; HttpOnly", "; Secure"} {
			if !strings.Contains(h, attr) {
				t.Errorf("cookie %q lacks %q", h, attr)
			}
		}
		if strings.Contains(h, "Domain=") || (sameSite == "") == strings.Contains(h, "SameSite") || !strings.Contains(h, sameSite) {
			t.Errorf("cookie %q: wrong Domain or SameSite", h)
		}
	}

	// Refused without this browser's cookie: the state stays unconsumed.
	other := strings.Repeat("ef", 32)
	for _, r := range []*http.Request{
		callbackRequest(state, "code"),
		withStateCookie(callbackRequest(state, "code"), other, false),
		withStateCookie(callbackRequest(other, "code"), state, false),
		withStateCookie(callbackRequest(state[:63], "code"), state[:63], false),
		callbackRequest(strings.Repeat("zz", 24), "code"), // an older node's length, not its shape
	} {
		a, mock := testAccounts(t)
		newFakeIdP(t, a)
		mock.ExpectBegin()
		w := httptest.NewRecorder()
		a.OAuthCallback(w, r)
		if loc := w.Header().Get("Location"); w.Code != http.StatusFound || loc != "/account?error="+cSignInExpired || sessionCookie(w) != "" {
			t.Errorf("a callback this browser didn't start: %d %q", w.Code, loc)
		}
		if mock.ExpectationsWereMet() == nil {
			t.Error("the state was consumed")
		}
	}

	// Either cookie alone lets the sign-in through, and both are cleared.
	for _, legacyOnly := range []bool{false, true} {
		a, mock := testAccounts(t)
		newFakeIdP(t, a)
		expectStateConsumed(mock, state, "")
		mock.ExpectQuery("from `accounts` where `id` = \\(select `account_id` from `account_logins`").WillReturnRows(sqlmock.NewRows(accountCols).
			AddRow("acc1", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now(), ""))
		epochRow(mock, 0)
		mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
		w := httptest.NewRecorder()
		a.OAuthCallback(w, withStateCookie(callbackRequest(state, "code"), state, legacyOnly))
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/account" || !strings.HasPrefix(sessionCookie(w), "acc1|0|") {
			t.Errorf("legacy cookie %v: %d %q", legacyOnly, w.Code, w.Header().Get("Location"))
		}
		cleared := 0
		for _, c := range w.Result().Cookies() {
			if (c.Name == name || c.Name == legacy) && c.MaxAge < 0 {
				cleared++
			}
		}
		if cleared != 2 {
			t.Errorf("%d of the state's cookies cleared, want 2", cleared)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	}
}

// A node still on the release before the state cookie (the cluster is
// deployed one node at a time) stores a 48-character state and sets no
// cookie. Its sign-in can come back to a node on this release, which lets
// it through as before; this release never makes that shape (stateCapture).
func TestOAuthStateFromAnOlderNode(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	state := strings.Repeat("12", 24)
	expectStateConsumed(mock, state, "")
	mock.ExpectQuery("from `accounts` where `id` = \\(select `account_id` from `account_logins`").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now(), ""))
	epochRow(mock, 0)
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
	w := httptest.NewRecorder()
	a.OAuthCallback(w, callbackRequest(state, "code"))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/account" || !strings.HasPrefix(sessionCookie(w), "acc1|0|") {
		t.Errorf("a sign-in an older node started: %d %q", w.Code, w.Header().Get("Location"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// stateCapture records the state OAuthStart stores.
type stateCapture struct{ to *string }

func (s stateCapture) Match(v driver.Value) bool {
	str, ok := v.(string)
	*s.to = str
	return ok && isOAuthState(str)
}

// Each sign-in start writes a row: one address gets cOAuthStartsPerMinute
// at once, then 429 - while a start refused for its return address costs
// nothing.
func TestOAuthStartIsRateLimited(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	a.oauthStarts = limits.NewRate(cOAuthStartsPerMinute/60.0, cOAuthStartsPerMinute)
	start := func(ret string) int {
		r := httptest.NewRequest("GET", "/account/auth/google/start?return="+url.QueryEscape(ret), nil)
		r.RemoteAddr = "203.0.113.7:4444"
		r.SetPathValue("provider", "google")
		w := httptest.NewRecorder()
		a.OAuthStart(w, r)
		return w.Code
	}
	for i := 0; i < 5; i++ {
		if code := start("https://evil.com/"); code != http.StatusBadRequest {
			t.Fatalf("a bad return address: %d", code)
		}
	}
	for i := 0; i < cOAuthStartsPerMinute; i++ {
		mock.ExpectExec("insert into `oauth_states`").WillReturnResult(sqlmock.NewResult(1, 1))
		if code := start(""); code != http.StatusFound {
			t.Fatalf("start %d: %d", i+1, code)
		}
	}
	if code := start(""); code != http.StatusTooManyRequests {
		t.Fatalf("start %d: %d, want 429", cOAuthStartsPerMinute+1, code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A refetch of a provider's keys that fails (the provider down, an error
// page, a set without keys) leaves the last good keys verifying sign-ins
// for up to cJWKSMaxStale, asking again at most every cJWKSRetry; past
// that, and for a key id the last set didn't have, the sign-in fails.
func TestJWKSKeepsTheLastGoodKeys(t *testing.T) {
	key := &rsa.PublicKey{N: big.NewInt(3233), E: 17}
	now := time.Unix(1_000_000, 0)
	var fail error
	fetches := 0
	c := &jwksCache{
		now: func() time.Time { return now },
		fetch: func(string) (map[string]*rsa.PublicKey, error) {
			fetches++
			if fail != nil {
				return nil, fail
			}
			return map[string]*rsa.PublicKey{"k1": key}, nil
		},
	}
	if k, err := c.key("https://idp/jwks", "k1"); err != nil || k != key {
		t.Fatalf("first fetch: %v %v", k, err)
	}
	now = now.Add(2 * cJWKSFresh)
	fail = errors.New("provider down")
	if k, err := c.key("https://idp/jwks", "k1"); err != nil || k != key || fetches != 2 {
		t.Fatalf("a failed refetch: %v %v after %d fetches, want the last good key", k, err, fetches)
	}
	now = now.Add(cJWKSRetry / 2)
	if k, err := c.key("https://idp/jwks", "k1"); err != nil || k != key || fetches != 2 {
		t.Fatalf("within cJWKSRetry: %v %v after %d fetches, want the key and no fetch", k, err, fetches)
	}
	if _, err := c.key("https://idp/jwks", "k2"); err == nil {
		t.Fatal("a key id no set had verified")
	}
	now = now.Add(cJWKSMaxStale)
	if k, err := c.key("https://idp/jwks", "k1"); err == nil || fetches != 3 {
		t.Fatalf("past cJWKSMaxStale: %v %v after %d fetches, want an error", k, err, fetches)
	}
	fail = nil
	now = now.Add(cJWKSRetry)
	if k, err := c.key("https://idp/jwks", "k1"); err != nil || k != key || fetches != 4 {
		t.Fatalf("the provider back: %v %v after %d fetches", k, err, fetches)
	}
}

// What isn't a key set - an error status, a set without an RSA key, a
// page - is a failed fetch, so it never replaces the last good keys.
func TestFetchJWKSRefusesWhatIsNotAKeySet(t *testing.T) {
	const good = `{"keys":[{"kty":"RSA","kid":"k1","n":"DKE","e":"EQ"}]}`
	answers := map[string]struct {
		status int
		body   string
	}{
		"/error":   {http.StatusServiceUnavailable, good},
		"/empty":   {http.StatusOK, `{"keys":[]}`},
		"/ec-only": {http.StatusOK, `{"keys":[{"kty":"EC","kid":"k1"}]}`},
		"/page":    {http.StatusOK, `<html>down for maintenance</html>`},
		"/good":    {http.StatusOK, good},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := answers[r.URL.Path]
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	defer srv.Close()
	for _, path := range []string{"/error", "/empty", "/ec-only", "/page"} {
		if keys, err := fetchJWKS(srv.URL + path); err == nil {
			t.Errorf("%s: %v, want an error", path, keys)
		}
	}
	keys, err := fetchJWKS(srv.URL + "/good")
	if err != nil || keys["k1"] == nil || keys["k1"].N.Int64() != 3233 || keys["k1"].E != 17 {
		t.Errorf("/good: %v %v", keys, err)
	}
}

// A fetch runs outside the lock, one per provider at a time: while one
// provider answers slowly, another provider's sign-in and one of its own
// with a key in hand go on at once, and one without a key waits for that
// same fetch instead of starting another.
func TestJWKSFetchHoldsUpOnlyWhatHasNoKey(t *testing.T) {
	key := &rsa.PublicKey{N: big.NewInt(3233), E: 17}
	var mu sync.Mutex
	now := time.Unix(1_000_000, 0)
	fetches := map[string]int{}
	slow := false
	started, release := make(chan struct{}, 1), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	c := &jwksCache{
		now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		fetch: func(u string) (map[string]*rsa.PublicKey, error) {
			mu.Lock()
			fetches[u]++
			block := slow && u == "apple"
			mu.Unlock()
			if block {
				select {
				case started <- struct{}{}:
				default: // a second fetch: counted, and the test says so
				}
				<-release
			}
			return map[string]*rsa.PublicKey{"k1": key}, nil
		},
	}
	if _, err := c.key("apple", "k1"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	now = now.Add(2 * cJWKSFresh) // Apple's keys are due for a refetch
	slow = true
	mu.Unlock()
	if _, err := c.key("google", "k1"); err != nil {
		t.Fatal(err)
	}

	refetched := make(chan error, 1)
	go func() { _, err := c.key("apple", "k1"); refetched <- err }()
	<-started // Apple's refetch is under way, and hangs
	within := func(what string, f func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- f() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: %v", what, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s waited for Apple's slow fetch", what)
		}
	}
	within("Google's sign-in", func() error { _, err := c.key("google", "k1"); return err })
	within("an Apple sign-in with its last key", func() error {
		k, err := c.key("apple", "k1")
		if err == nil && k != key {
			err = errors.New("another key")
		}
		return err
	})
	noKey := make(chan error, 1)
	go func() { _, err := c.key("apple", "k2"); noKey <- err }()

	releaseOnce()
	if err := <-refetched; err != nil {
		t.Errorf("the refetch: %v", err)
	}
	if err := <-noKey; err == nil {
		t.Error("an unknown key id verified")
	}
	mu.Lock()
	defer mu.Unlock()
	if fetches["apple"] != 2 || fetches["google"] != 1 {
		t.Errorf("fetches %v, want apple 2 (no second one while one was under way), google 1", fetches)
	}
}

// A sign-in start is a page the browser opens. An image, frame or fetch
// another site fires at it is refused before it writes a state, sets a
// cookie or uses up the address's starts; with no Sec-Fetch headers
// (older Safari) it goes through.
func TestOAuthStartOnlyAsAPage(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	a.oauthStarts = limits.NewRate(cOAuthStartsPerMinute/60.0, cOAuthStartsPerMinute)
	start := func(h map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/account/auth/google/start", nil)
		r.RemoteAddr = "203.0.113.7:4444"
		r.SetPathValue("provider", "google")
		for k, v := range h {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		a.OAuthStart(w, r)
		return w
	}
	for _, h := range []map[string]string{
		{"Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image", "Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe", "Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"},
		{"Sec-Fetch-Mode": "no-cors"},
		{"Sec-Fetch-Dest": "script"},
	} {
		for i := 0; i <= cOAuthStartsPerMinute; i++ {
			if w := start(h); w.Code != http.StatusBadRequest || len(w.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("%v: %d, cookies %q", h, w.Code, w.Header().Values("Set-Cookie"))
			}
		}
	}
	for _, h := range []map[string]string{
		{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "same-origin"},
		{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "cross-site"}, // a device's page, an app's sheet
		{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "none"},
		nil,
	} {
		mock.ExpectExec("insert into `oauth_states`").WillReturnResult(sqlmock.NewResult(1, 1))
		if w := start(h); w.Code != http.StatusFound || len(w.Header().Values("Set-Cookie")) != 2 {
			t.Errorf("%v: %d, cookies %q", h, w.Code, w.Header().Values("Set-Cookie"))
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
