// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/tls"
	"database/sql/driver"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// signInBed is the whole of a provider sign-in a device's web app starts,
// over HTTPS: the bridge's sign-in routes, a provider that answers
// without asking anything (someone who has signed in with it before), and
// the device's page on the home network - BridgePanel.tsx, which reads
// setup_token from its own address - as a browser with a cookie jar sees
// them.
type signInBed struct {
	a      *Accounts
	mock   sqlmock.Sqlmock
	bridge *httptest.Server
	// browser follows redirects; stopAt doesn't (it shows each step).
	browser, stopAt *http.Client
	// back is where BridgePanel's startProvider asks to come back to: its
	// own address with ?bridge_signin=1 (a loopback address, which the
	// bridge allows like a private one).
	back string
	// visits are the device page's addresses as the browser opened them.
	visits chan *url.URL
}

func newSignInBed(t *testing.T) *signInBed {
	t.Helper()
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	b := &signInBed{a: a, mock: mock, visits: make(chan *url.URL, 8)}
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.visits <- r.URL
		_, _ = io.WriteString(w, "the device's web app")
	}))
	t.Cleanup(device.Close)
	b.back = device.URL + "/?bridge_signin=1"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /account/auth/{provider}/start", a.OAuthStart)
	mux.HandleFunc("GET /account/auth/{provider}/callback", a.OAuthCallback)
	mux.HandleFunc("GET /api/account/continue", a.RequireAuth(a.ContinueSetup))
	mux.HandleFunc("GET "+ContinuePath, a.ConfirmSetup)
	b.bridge = httptest.NewTLSServer(mux)
	t.Cleanup(b.bridge.Close)
	// The bridge's own host: its redirect URI, and what its pages' links
	// are checked against.
	a.tld = strings.TrimPrefix(b.bridge.URL, "https://")

	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		http.Redirect(w, r, q.Get("redirect_uri")+"?state="+url.QueryEscape(q.Get("state"))+"&code=the-code", http.StatusFound)
	}))
	t.Cleanup(idp.Close)
	a.providers["google"].authURL = idp.URL + "/auth"

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: b.bridge.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}}
	b.browser = &http.Client{Jar: jar, Transport: transport}
	b.stopAt = &http.Client{Jar: jar, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return b
}

// open is the browser opening target as a page: a link followed, from the
// bridge's own page (sameOrigin) or another site's.
func (b *signInBed) open(t *testing.T, c *http.Client, target string, sameOrigin bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		t.Fatal(err)
	}
	site := "cross-site"
	if sameOrigin {
		site = "same-origin"
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	req.Header.Set("Sec-Fetch-Site", site)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "document")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

// start is BridgePanel's startProvider: the page on the home network sends
// the browser to the bridge to sign in with Google and come back.
func (b *signInBed) start(t *testing.T, c *http.Client) (*http.Response, string) {
	return b.open(t, c, b.bridge.URL+"/account/auth/google/start?return="+url.QueryEscape(b.back), false)
}

// expectSignIn is the database of a sign-in from its start to its callback
// for acc1, whose profile is complete or not.
func (b *signInBed) expectSignIn(complete bool) {
	b.mock.ExpectExec("insert into `oauth_states`").WithArgs(sqlmock.AnyArg(), b.back, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	b.mock.ExpectBegin()
	b.mock.ExpectQuery("select `return_url`, `created` from `oauth_states`").
		WillReturnRows(sqlmock.NewRows([]string{"return_url", "created"}).AddRow(b.back, time.Now()))
	b.mock.ExpectExec("delete from `oauth_states`").WillReturnResult(sqlmock.NewResult(0, 1))
	b.mock.ExpectCommit()
	country := "ES"
	if !complete {
		country = ""
	}
	b.mock.ExpectQuery("from `accounts` where `id` = \\(select `account_id` from `account_logins`").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", country, nil, time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now(), ""))
	epochRow(b.mock, 0)
	b.mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectToken is the database of a Continue that sends acc1's browser
// back with a setup token: its session checked, the token stored (into
// *token).
func (b *signInBed) expectToken(token *string) {
	epochRow(b.mock, 0)
	verifiedRow(b.mock, "acc1", true)
	b.mock.ExpectExec("insert into `account_tokens`").WithArgs(tokenCapture{token}, "acc1", cPurposeSetup, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
}

// tokenCapture records the setup token stored.
type tokenCapture struct{ to *string }

func (c tokenCapture) Match(v driver.Value) bool {
	s, ok := v.(string)
	*c.to = s
	return ok && len(s) == cSetupCodeLen
}

var continueLink = regexp.MustCompile(`<a class="btn" href="(` + regexp.QuoteMeta(ContinuePath) + `\?id=[0-9a-f]{32})">Continue</a>`)

// checkQuestion checks the question page a sign-in stopped on: the
// device's address named, nothing sent to it yet, under the site's policy
// and never in a frame; and returns its Continue link.
func (b *signInBed) checkQuestion(t *testing.T, resp *http.Response, body string) string {
	t.Helper()
	host := strings.TrimPrefix(strings.TrimSuffix(b.back, "/?bridge_signin=1"), "http://")
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Host != b.a.tld {
		t.Fatalf("the sign-in ended on %d %s, want the question on the bridge", resp.StatusCode, resp.Request.URL)
	}
	for _, want := range []string{
		"<title>Continue the setup — Off The Cloud</title>",
		"<h1>Continue setting up the device at <strong>" + host + "</strong>?</h1>",
		"Continue sends a setup code for your account to " + host + ".",
		`<a class="btn ghost" href="/account">Cancel</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the question page has no %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "setup_token") || strings.Contains(body, b.back) {
		t.Error("the question page carries the setup token or the address to send it to")
	}
	h := resp.Header
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "default-src 'none'") ||
		!strings.Contains(csp, "script-src 'nonce-") || h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("the question page's policy %q, X-Frame-Options %q", csp, h.Get("X-Frame-Options"))
	}
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Errorf("the question page's headers: %v", h)
	}
	select {
	case u := <-b.visits:
		t.Fatalf("the device's page was opened before Continue: %s", u)
	default:
	}
	m := continueLink.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the question page has no Continue link:\n%s", body)
	}
	return m[1]
}

// checkBack checks that the browser came back to the device's page with
// token, as BridgePanel reads it.
func (b *signInBed) checkBack(t *testing.T, resp *http.Response, body, token string) {
	t.Helper()
	if resp.StatusCode != http.StatusOK || body != "the device's web app" {
		t.Fatalf("Continue ended on %d %q", resp.StatusCode, body)
	}
	select {
	case u := <-b.visits:
		if q := u.Query(); q.Get("setup_token") != token || q.Get("bridge_signin") != "1" || token == "" {
			t.Errorf("the device's page was opened at %s, want bridge_signin=1 and the stored token %q", u, token)
		}
	default:
		t.Fatal("Continue never opened the device's page")
	}
}

// Audit F4, end to end: a sign-in the device's web app starts stops on the
// bridge's question, which names the device's address; only its Continue
// takes the browser back there with a setup token - the account's, made
// only then - and only once.
func TestProviderSignInAsksBeforeTheSetupCode(t *testing.T) {
	b := newSignInBed(t)
	b.expectSignIn(true)
	resp, body := b.start(t, b.browser)
	link := b.checkQuestion(t, resp, body)
	if err := b.mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("before Continue: %v", err)
	}

	var token string
	b.expectToken(&token)
	resp, body = b.open(t, b.browser, b.bridge.URL+link, true)
	b.checkBack(t, resp, body, token)
	if err := b.mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	// Used once: the same Continue again sends nothing.
	resp, body = b.open(t, b.browser, b.bridge.URL+link, true)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "This step has expired") && !strings.Contains(body, "this step has expired") {
		t.Errorf("Continue again: %d %s", resp.StatusCode, body)
	}
	select {
	case u := <-b.visits:
		t.Errorf("Continue again opened the device's page: %s", u)
	default:
	}
}

// A new Google account (or one whose details aren't complete) fills them
// in on the account page first; "Save and continue" then asks the same
// question, for the address the sign-in came from, and Continue goes back
// with the code.
func TestProviderSignInAsksAfterTheProfile(t *testing.T) {
	b := newSignInBed(t)
	b.expectSignIn(false)
	resp, _ := b.start(t, b.stopAt) // the start: off to the provider
	for resp.StatusCode == http.StatusFound && !strings.HasPrefix(resp.Header.Get("Location"), "/account?") {
		next, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		resp, _ = b.open(t, b.stopAt, resp.Request.URL.ResolveReference(next).String(), false)
	}
	want := "/account?" + url.Values{"complete": {"1"}, "return": {b.back}}.Encode()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
		t.Fatalf("the callback: %d %q, want the profile first (%s)", resp.StatusCode, resp.Header.Get("Location"), want)
	}
	if err := b.mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the sign-in: %v", err)
	}

	// The account page's Save and continue (after the profile is saved).
	epochRow(b.mock, 0) // RequireAuth
	resp, body := b.open(t, b.browser, b.bridge.URL+"/api/account/continue?return="+url.QueryEscape(b.back), true)
	link := b.checkQuestion(t, resp, body)
	var token string
	b.expectToken(&token)
	resp, body = b.open(t, b.browser, b.bridge.URL+link, true)
	b.checkBack(t, resp, body, token)
	if err := b.mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// grantCookie is a request cookie holding a grant for accountID to send a
// setup code to returnURL, made as the callback makes it.
func grantCookie(t *testing.T, a *Accounts, accountID, returnURL string, at time.Time) (string, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	id, err := a.grantSetup(w, accountID, returnURL, at)
	if err != nil {
		t.Fatal(err)
	}
	cs := w.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != cSetupGrantCookie+id || !cs[0].Secure || !cs[0].HttpOnly || cs[0].Path != "/" ||
		cs[0].SameSite != http.SameSiteLaxMode || cs[0].MaxAge != int(cSetupGrantTTL.Seconds()) || cs[0].Domain != "" {
		t.Fatalf("the grant's cookie: %+v", cs)
	}
	return id, &http.Cookie{Name: cs[0].Name, Value: cs[0].Value}
}

// Continue sends the code only for the link followed on the bridge's own
// page, in the browser holding its grant, signed in to the grant's
// account, within cSetupGrantTTL: anything else is the sign-in's error
// page and no token - and no database call before the grant checks out.
func TestSetupContinueRefusals(t *testing.T) {
	const back = "http://192.168.1.20:8080/?bridge_signin=1"
	now := time.Now()
	type try struct {
		what    string
		headers map[string]string
		grant   func(a *Accounts) (string, *http.Cookie)
		session string // the session cookie's account, "" for none
		status  int
		db      func(mock sqlmock.Sqlmock)
	}
	navigate := map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Accept": "text/html"}
	with := func(extra map[string]string) map[string]string {
		h := map[string]string{}
		for k, v := range navigate {
			h[k] = v
		}
		for k, v := range extra {
			if v == "" {
				delete(h, k)
			} else {
				h[k] = v
			}
		}
		return h
	}
	mine := func(a *Accounts) (string, *http.Cookie) { return grantCookie(t, a, "acc1", back, now) }
	for _, c := range []try{
		{"another site's link", with(map[string]string{"Sec-Fetch-Site": "cross-site"}), mine, "acc1", http.StatusForbidden, nil},
		{"a device's page on a subdomain", with(map[string]string{"Sec-Fetch-Site": "same-site"}), mine, "acc1", http.StatusForbidden, nil},
		{"typed or from a bookmark", with(map[string]string{"Sec-Fetch-Site": "none"}), mine, "acc1", http.StatusForbidden, nil},
		{"in a frame", with(map[string]string{"Sec-Fetch-Dest": "iframe"}), mine, "acc1", http.StatusForbidden, nil},
		{"a fetch", with(map[string]string{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}), mine, "acc1", http.StatusForbidden, nil},
		{"a prefetch", with(map[string]string{"Sec-Purpose": "prefetch"}), mine, "acc1", http.StatusForbidden, nil},
		{"no grant", navigate, func(*Accounts) (string, *http.Cookie) { return strings.Repeat("ab", 16), nil }, "acc1", http.StatusBadRequest, nil},
		{"another grant's id", navigate, func(a *Accounts) (string, *http.Cookie) {
			_, c := mine(a)
			return strings.Repeat("cd", 16), c
		}, "acc1", http.StatusBadRequest, nil},
		{"a grant under another id's name", navigate, func(a *Accounts) (string, *http.Cookie) {
			_, c := mine(a)
			other := strings.Repeat("ef", 16)
			return other, &http.Cookie{Name: cSetupGrantCookie + other, Value: c.Value}
		}, "acc1", http.StatusBadRequest, nil},
		{"a tampered grant", navigate, func(a *Accounts) (string, *http.Cookie) {
			id, c := mine(a)
			enc, sig, _ := strings.Cut(c.Value, ".")
			raw, _ := base64.RawURLEncoding.DecodeString(enc)
			forged := strings.Replace(string(raw), "192.168.1.20", "192.168.1.66", 1)
			return id, &http.Cookie{Name: c.Name, Value: base64.RawURLEncoding.EncodeToString([]byte(forged)) + "." + sig}
		}, "acc1", http.StatusBadRequest, nil},
		{"an expired grant", navigate, func(a *Accounts) (string, *http.Cookie) {
			return grantCookie(t, a, "acc1", back, now.Add(-cSetupGrantTTL-time.Second))
		}, "acc1", http.StatusBadRequest, nil},
		{"signed out", navigate, mine, "", http.StatusBadRequest, nil},
		{"signed in to another account", navigate, mine, "acc2", http.StatusBadRequest, func(mock sqlmock.Sqlmock) { epochRow(mock, 0) }},
		{"the session check failing", navigate, mine, "acc1", http.StatusInternalServerError, func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery("select `session_epoch` from `accounts`").WillReturnError(errDBDown)
		}},
	} {
		a, mock := testAccounts(t)
		a.tld = "off-the.cloud"
		id, grant := c.grant(a)
		r := httptest.NewRequest("GET", ContinuePath+"?id="+id, nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if grant != nil {
			r.AddCookie(grant)
		}
		if c.session != "" {
			r.AddCookie(&http.Cookie{Name: cSessionCookie, Value: a.sessionToken(c.session, 0, now)})
		}
		if c.db != nil {
			c.db(mock)
		}
		w := httptest.NewRecorder()
		a.ConfirmSetup(w, r)
		if w.Code != c.status || w.Header().Get("Location") != "" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %q %q, want the %d page", c.what, w.Code, w.Header().Get("Location"), w.Header().Get("Content-Type"), c.status)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", c.what, err)
		}
	}

	// Older Safari sends no Sec-Fetch headers: the Referer of the bridge's
	// own page stands in, and the grant does the rest.
	a, mock := testAccounts(t)
	a.tld = "off-the.cloud"
	id, grant := grantCookie(t, a, "acc1", back, now)
	r := httptest.NewRequest("GET", ContinuePath+"?id="+id, nil)
	r.Header.Set("Referer", "https://off-the.cloud/account/auth/apple/callback")
	r.AddCookie(grant)
	r.AddCookie(&http.Cookie{Name: cSessionCookie, Value: a.sessionToken("acc1", 0, now)})
	epochRow(mock, 0)
	verifiedRow(mock, "acc1", true)
	mock.ExpectExec("insert into `account_tokens`").WillReturnResult(sqlmock.NewResult(1, 1))
	w := httptest.NewRecorder()
	a.ConfirmSetup(w, r)
	if loc := w.Header().Get("Location"); w.Code != http.StatusFound || !strings.HasPrefix(loc, back+"&setup_token=") {
		t.Errorf("Safari's Continue: %d %q", w.Code, loc)
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		cleared = cleared || (c.Name == grant.Name && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("the grant was not used up")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Only a sign-in coming back to a device's page asks; the account page's
// own and the apps' (whose code needs the app's verifier) go on as before.
func TestNeedsConfirm(t *testing.T) {
	for ret, want := range map[string]bool{
		"": false,
		AppReturn + "?challenge=" + strings.Repeat("a", 43): false,
		"http://192.168.1.20:8080/?bridge_signin=1":         true,
		"http://otc.local:8080/":                            true,
		"https://pit.otc/":                                  true,
	} {
		if got := needsConfirm(ret); got != want {
			t.Errorf("needsConfirm(%q) = %v", ret, got)
		}
	}
}
