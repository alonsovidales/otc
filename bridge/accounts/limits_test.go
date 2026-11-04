// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"encoding/json"
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
// the budget, a link email isn't sent (a resend is a 429 with Retry-After),
// and the new-account note is dropped first.
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
