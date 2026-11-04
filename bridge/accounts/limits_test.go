// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/limits"
	"github.com/alonsovidales/otc/i18n"
	"golang.org/x/crypto/bcrypt"
)

// Sign-ups are bounded per address, per network - an IPv6 /48 is 65,536
// /64s, each an address's bucket of its own - and on the whole node: past
// any of them a sign-up is a 429 with Retry-After, before any work.
func TestSignupsAreBoundedPerNetworkAndNode(t *testing.T) {
	a, mock := testAccounts(t)
	a.signups = limits.NewRate(1.0/3600, 5)
	a.signupsPerNet = limits.NewRate(1.0/3600, 3)
	a.signupsAll = limits.NewRate(1.0/3600, 4)
	signup := func(remote string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/account/signup", strings.NewReader(`not json`))
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		a.Signup(w, r)
		return w
	}
	// Three /64s of one /48 get through (to the body check), the fourth
	// not; another /48 gets the node's last one, then nobody does.
	for i := 1; i <= 3; i++ {
		if w := signup(fmt.Sprintf("[2001:db8:1:%x::1]:1000", i)); w.Code != http.StatusBadRequest {
			t.Fatalf("sign-up %d of the /48: %d", i, w.Code)
		}
	}
	for _, remote := range []string{"[2001:db8:1:99::1]:1000", "[2001:db8:2::1]:1000", "198.51.100.7:1000"} {
		w := signup(remote)
		if remote == "[2001:db8:2::1]:1000" {
			if w.Code != http.StatusBadRequest {
				t.Errorf("another /48: %d", w.Code)
			}
			continue
		}
		if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"too_many_signups"`) || w.Header().Get("Retry-After") == "" {
			t.Errorf("%s: %d %s, Retry-After %q", remote, w.Code, w.Body.String(), w.Header().Get("Retry-After"))
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Failed passwords count per network too: a guesser that moves to a new
// /64 of its /48 after every few guesses is locked out once the network
// has failed cLoginNetFailures times, while another network isn't.
func TestLoginFailuresCountPerNetwork(t *testing.T) {
	a, _ := testAccounts(t)
	now := time.Now()
	for i := 0; i < cLoginNetFailures; i++ {
		key := fmt.Sprintf("2001:db8:1:%x::/64", i)
		if ok, _ := a.loginAllowed(key, now); !ok {
			t.Fatalf("locked out after %d failures of the network", i)
		}
		a.loginFailed(key, now)
	}
	ok, wait := a.loginAllowed("2001:db8:1:ffff::/64", now)
	if ok || wait <= 0 || wait > cLoginWindow {
		t.Errorf("a fresh /64 of a network past its failures: allowed %v, wait %s", ok, wait)
	}
	if ok, _ := a.loginAllowed("2001:db8:2:1::/64", now); !ok {
		t.Error("another network was locked out")
	}
	if ok, _ := a.loginAllowed("2001:db8:1:ffff::/64", now.Add(cLoginWindow)); !ok {
		t.Error("the network stayed locked out past the window")
	}
	a.limiterMu.Lock()
	a.netFailures.prune(now.Add(2*cLoginWindow), cLoginWindow)
	left := len(a.netFailures.m)
	a.limiterMu.Unlock()
	if left != 0 {
		t.Errorf("%d networks left after their failures aged out", left)
	}
}

// The emails strangers can have sent are bounded on the whole node: past
// the budget, a link email isn't sent (a resend is a 429 with Retry-After).
// The new-account note goes first: TestSignupConfirmsBeforeTheNote.
func TestEmailBudget(t *testing.T) {
	a, mock := testAccounts(t)
	a.emailBudget = limits.NewRate(1.0/3600, 1)
	sent := 0
	a.send = func(to, subject, body string) error { sent++; return nil }
	acc := &dao.Account{ID: "acc1", Email: "a@b.c", Name: "A"}
	link := func() {
		mock.ExpectQuery("select max\\(`created`\\) from `account_email_tokens`").WillReturnRows(sqlmock.NewRows([]string{"t"}).AddRow(nil))
	}
	link()
	mock.ExpectExec("insert into `account_email_tokens`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("delete from `account_email_tokens`").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := a.sendVerification(acc, ""); err != nil || sent != 1 {
		t.Fatalf("within the budget: %v, %d sent", err, sent)
	}
	link()
	if err := a.sendVerification(acc, ""); err != errEmailBudget || sent != 1 {
		t.Fatalf("past the budget: %v, %d sent", err, sent)
	}
	verifiedRow(mock, "acc1", false)
	link()
	w := httptest.NewRecorder()
	a.ResendVerification(w, httptest.NewRequest("POST", "/api/account/resend-verification", nil), "acc1")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"too_many_emails"`) || w.Header().Get("Retry-After") == "" {
		t.Errorf("a resend past the budget: %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A confirmation email goes to an address whoever signs up picks, so it
// greets nobody by the name they typed - text of their choosing in the
// first line was spam waiting to be sent; once the address is proven the
// reset email greets its owner as before.
func TestEmailsToUnprovenAddressesHaveNoName(t *testing.T) {
	a, mock := testAccounts(t)
	a.tld = "off-the.cloud"
	var bodies []string
	a.send = func(to, subject, body string) error { bodies = append(bodies, body); return nil }
	for _, acc := range []*dao.Account{
		{ID: "acc1", Email: "victim@example.com", Name: "Cheap pills at example.com", EmailVerified: false},
		{ID: "acc2", Email: "ann@example.com", Name: "Ann", EmailVerified: true},
	} {
		mock.ExpectQuery("select max\\(`created`\\) from `account_email_tokens`").WillReturnRows(sqlmock.NewRows([]string{"t"}).AddRow(nil))
		mock.ExpectExec("insert into `account_email_tokens`").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("delete from `account_email_tokens`").WillReturnResult(sqlmock.NewResult(0, 0))
		var err error
		if acc.EmailVerified {
			err = a.sendReset(acc)
		} else {
			err = a.sendVerification(acc, "")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 || !strings.HasPrefix(bodies[0], "Hello,\n") || strings.Contains(bodies[0], "pills") || !strings.HasPrefix(bodies[1], "Hello Ann,\n") {
		t.Errorf("bodies: %q", bodies)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A request whose password hashing found the node busy is a 503 to try
// again in a moment, with its handler's own code; any other failure a 500.
func TestBusyHashingIsTryAgain(t *testing.T) {
	w := httptest.NewRecorder()
	hashRefused(w, limits.ErrBusy, "signin_unavailable", i18n.ApiErrorSigninUnavailable())
	var out struct{ Code, Error string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusServiceUnavailable || out.Code != "signin_unavailable" || w.Header().Get("Retry-After") != "5" {
		t.Errorf("busy: %d %s, Retry-After %q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	w = httptest.NewRecorder()
	hashRefused(w, errDBDown, "signin_unavailable", i18n.ApiErrorSigninUnavailable())
	if w.Code != http.StatusInternalServerError || w.Header().Get("Retry-After") != "" {
		t.Errorf("another failure: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}

// The requests that ask the database whether what a stranger sends is real
// - a setup code (setup-token-info), an email link (verify, reset), an
// app's sign-in code (app-exchange), a Google or Apple sign-in coming back
// - share one bound per address and one per network, taken before the
// database is asked: past either, a 429 with Retry-After and no query.
func TestLookupsAreBoundedBeforeTheDatabase(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	a.lookups = limits.NewRate(1.0/3600, 2)
	a.lookupsPerNet = limits.NewRate(1.0/3600, 3)
	from := func(r *http.Request, remote string) *http.Request {
		r.RemoteAddr = remote
		return r
	}
	info := func(remote string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.SetupTokenInfo(w, from(httptest.NewRequest("GET", "/api/account/setup-token-info?token=ABCD-EFGH", nil), remote))
		return w
	}
	unknownCode := func() {
		mock.ExpectQuery("from `account_setup_codes`").WillReturnRows(sqlmock.NewRows([]string{"account_id", "owner_confirmed"}))
		mock.ExpectQuery("from `account_tokens`").WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	}
	refused := func(what string, w *httptest.ResponseRecorder, json bool) {
		t.Helper()
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || json && !strings.Contains(w.Body.String(), `"too_many_attempts"`) {
			t.Errorf("%s: %d %s, Retry-After %q", what, w.Code, w.Body.String(), w.Header().Get("Retry-After"))
		}
	}

	// An address's first two reach the database...
	for i := 1; i <= 2; i++ {
		unknownCode()
		if w := info("203.0.113.7:1000"); w.Code != http.StatusNotFound {
			t.Fatalf("lookup %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	// ... then none of them does, from any port.
	post := func(handler func(http.ResponseWriter, *http.Request), path, body string) func(string) *httptest.ResponseRecorder {
		return func(remote string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			handler(w, from(httptest.NewRequest("POST", path, strings.NewReader(body)), remote))
			return w
		}
	}
	for _, l := range []struct {
		name string
		call func(string) *httptest.ResponseRecorder
	}{
		{"setup-token-info", info},
		{"verify", post(a.Verify, "/api/account/verify", `{"token":"a-link-token"}`)},
		{"reset", post(a.Reset, "/api/account/reset", `{"token":"a-link-token","password":"a long enough password"}`)},
		{"app-exchange", post(a.AppExchange, "/api/account/app-exchange", `{"code":"a-code","verifier":"a-verifier"}`)},
	} {
		refused(l.name, l.call("203.0.113.7:2000"), true)
	}
	// A sign-in coming back with a state of an older node's shape, which
	// needs no cookie: the sign-in pages' 429.
	w := httptest.NewRecorder()
	a.OAuthCallback(w, from(callbackRequest(strings.Repeat("12", 24), "code"), "203.0.113.7:3000"))
	refused("the sign-in callback", w, false)

	// Another address of the /24 has the network's last one, then the
	// network is spent; another network is not held up.
	unknownCode()
	if w := info("203.0.113.8:1000"); w.Code != http.StatusNotFound {
		t.Fatalf("another address of the /24: %d %s", w.Code, w.Body.String())
	}
	refused("the /24, spent", info("203.0.113.8:1000"), true)
	unknownCode()
	if w := info("198.51.100.4:1000"); w.Code != http.StatusNotFound {
		t.Errorf("another network: %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// An account makes at most cSetupCodesPerAccount setup codes in a code's
// lifetime: past it, asking for one is a 429 with Retry-After and nothing
// is stored - another account is not held up.
func TestSetupCodesAreCappedPerAccount(t *testing.T) {
	a, mock := testAccounts(t)
	a.codesPerAccount = limits.NewRate(cSetupCodesPerAccount/cSetupTokenTTL.Seconds(), cSetupCodesPerAccount)
	for i := 1; i <= cSetupCodesPerAccount; i++ {
		verifiedRow(mock, "acc1", true)
		expectSetupCodeStored(mock, "acc1", false, nil)
		if _, err := a.IssueSetupToken("acc1", false); err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
	}
	verifiedRow(mock, "acc1", true)
	if _, err := a.IssueSetupToken("acc1", false); !errors.Is(err, ErrTooManySetupCodes) {
		t.Fatalf("code %d: %v", cSetupCodesPerAccount+1, err)
	}
	verifiedRow(mock, "acc1", true)
	w := httptest.NewRecorder()
	a.SetupToken(w, httptest.NewRequest("GET", "/api/account/setup-token", nil), "acc1")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"setup_code_unavailable"`) || w.Header().Get("Retry-After") == "" {
		t.Errorf("the account page asking for one more: %d %s, Retry-After %q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	if got := w.Header().Get("Retry-After"); got != fmt.Sprint(int(cSetupTokenTTL.Seconds())/cSetupCodesPerAccount) {
		t.Errorf("Retry-After %s, want when the next is free", got)
	}
	verifiedRow(mock, "acc2", true)
	expectSetupCodeStored(mock, "acc2", false, nil)
	if _, err := a.IssueSetupToken("acc2", false); err != nil {
		t.Errorf("another account: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The requests that hash a password are bounded per address and per
// network before any work, the right passwords too: failures alone were
// counted only after their bcrypt, so many at once - or a stream of right
// ones for an account of one's own - kept the node's hashing slots full,
// and everyone else's sign-in got "busy". Past either bound, a 429 with
// Retry-After, before the database is asked.
func TestPasswordTriesAreBoundedPerAddressAndNetwork(t *testing.T) {
	a, mock := testAccounts(t)
	a.passwordTries = limits.NewRate(1.0/3600, 2)
	a.passwordTriesPerNet = limits.NewRate(1.0/3600, 3)
	hash, err := bcrypt.GenerateFromPassword([]byte("right-password"), limits.BcryptCost)
	if err != nil {
		t.Fatal(err)
	}
	login := func(remote, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/account/login", strings.NewReader(body))
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		a.Login(w, r)
		return w
	}
	const right = `{"email":"a@b.c","password":"right-password"}`
	for i := 0; i < 2; i++ {
		mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
			AddRow("acc1", "a@b.c", "A", "B", "ES", string(hash), time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now(), ""))
		epochRow(mock, 0)
		mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
		if w := login("[2001:db8:1:1::1]:1000", right); w.Code != http.StatusOK {
			t.Fatalf("sign-in %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	refused := func(what, remote string) {
		t.Helper()
		w := login(remote, right)
		if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"too_many_attempts"`) || w.Header().Get("Retry-After") == "" {
			t.Errorf("%s: %d %s, Retry-After %q", what, w.Code, w.Body.String(), w.Header().Get("Retry-After"))
		}
	}
	refused("a third right password from the /64", "[2001:db8:1:1::2]:1000")
	// Another /64 of the /48 gets the network's last one (a junk body: it
	// is counted before the body is read), the next none.
	if w := login("[2001:db8:1:2::1]:1000", "not json"); w.Code != http.StatusBadRequest {
		t.Errorf("another /64 of the /48: %d %s", w.Code, w.Body.String())
	}
	refused("a third /64 of the /48", "[2001:db8:1:3::1]:1000")
	if w := login("[2001:db8:2::1]:1000", "not json"); w.Code != http.StatusBadRequest {
		t.Errorf("another network: %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The other requests that hash a password count against the same bounds:
// the owner's confirmation, a password change, a reset.
func TestEveryPasswordHashIsBounded(t *testing.T) {
	a, mock := testAccounts(t)
	a.passwordTries = limits.NewRate(1.0/3600, 1)
	remote := "203.0.113.5:1000"
	req := func(method, target, body string) *http.Request {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.RemoteAddr = remote
		return r
	}
	a.passwordTries.Allow(clientIP(req("GET", "/", ""))) // the address's only one, spent
	withPassword := func() {
		mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
			AddRow("acc1", "a@b.c", "A", "B", "ES", "$2a$12$not-checked", time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now(), ""))
	}
	is429 := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"too_many_attempts"`) || w.Header().Get("Retry-After") == "" {
			t.Errorf("%s: %d %s", what, w.Code, w.Body.String())
		}
	}
	withPassword()
	w := httptest.NewRecorder()
	if a.ConfirmOwner(w, req("DELETE", "/api/account/me", ""), "acc1", "a-password") {
		t.Error("confirmed past the bound")
	}
	is429("ConfirmOwner", w)
	withPassword()
	w = httptest.NewRecorder()
	a.SetPassword(w, req("PUT", "/api/account/password", `{"password":"a-new-password","current":"a-password"}`), "acc1")
	is429("SetPassword", w)
	mock.ExpectQuery("from `account_email_tokens`").WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow("acc1"))
	w = httptest.NewRecorder()
	a.Reset(w, req("POST", "/api/account/reset", `{"token":"a-token","password":"a-new-password"}`))
	is429("Reset", w)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A sign-up sends the person's confirmation before the note to the
// project, and the note takes only what the email budget has to spare
// (cNoteReserve): with the budget nearly spent, the confirmation goes and
// the note doesn't; with room, both go.
func TestSignupConfirmsBeforeTheNote(t *testing.T) {
	for _, c := range []struct {
		budget   float64
		wantNote bool
	}{{1, false}, {cNoteReserve, false}, {cNoteReserve + 2, true}} {
		a, mock := testAccounts(t)
		a.emailBudget = limits.NewRate(1.0/3600, c.budget)
		a.noteTo = "info@example.org"
		sent := make(chan string, 4)
		a.send = func(to, subject, body string) error { sent <- to; return nil }
		mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(accountCols))
		mock.ExpectExec("insert into `accounts`").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectQuery("select max\\(`created`\\) from `account_email_tokens`").WillReturnRows(sqlmock.NewRows([]string{"t"}).AddRow(nil))
		mock.ExpectExec("insert into `account_email_tokens`").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("delete from `account_email_tokens`").WillReturnResult(sqlmock.NewResult(0, 0))
		epochRow(mock, 0)
		mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
		w := httptest.NewRecorder()
		a.Signup(w, signupRequest(`{`+cSignupBody+`}`, ""))
		if w.Code != http.StatusCreated {
			t.Fatalf("budget %v: sign-up %d %s", c.budget, w.Code, w.Body.String())
		}
		if to := <-sent; to != "new@example.com" {
			t.Errorf("budget %v: the first email went to %q", c.budget, to)
		}
		select {
		case to := <-sent:
			if !c.wantNote || to != "info@example.org" {
				t.Errorf("budget %v: an email to %q", c.budget, to)
			}
		case <-time.After(200 * time.Millisecond):
			if c.wantNote {
				t.Errorf("budget %v: no note", c.budget)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("budget %v: %v", c.budget, err)
		}
	}
}
