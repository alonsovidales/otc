// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

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
