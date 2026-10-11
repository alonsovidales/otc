// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// Localization (docs/i18n.md): accounts.lang, the language an account's
// emails are written in. These use languages that never ship (ja, zz) for
// "not carried", so they hold in a DRAFT build too.

func TestRequestLanguage(t *testing.T) {
	for _, c := range []struct {
		explicit, header, want string
	}{
		{"en", "", "en"},
		{"EN-us", "ja", "en"},
		{"zz", "en-GB,en;q=0.9", "en"}, // a page language we don't carry: the browser's
		{"", "en-GB,en;q=0.9", "en"},
		{"", "ja-JP,ja;q=0.9,en;q=0.5", "en"}, // the first one carried
		{"", "ja-JP,ja;q=0.9", ""},            // none carried: not known, never English by default
		{"", "en;q=0", ""},                    // "not English"
		{"", "*", ""},
		{"", "", ""},
		{"", "en-GB;q=1.0;garbage=%%%", ""},
		{"", "en," + strings.Repeat("x", cMaxAcceptLanguage), ""},
		{"<script>", "", ""},
	} {
		r := httptest.NewRequest("POST", "/api/account/signup", nil)
		if c.header != "" {
			r.Header.Set("Accept-Language", c.header)
		}
		if got := requestLanguage(c.explicit, r); got != c.want {
			t.Errorf("requestLanguage(%q, %q) = %q, want %q", c.explicit, c.header, got, c.want)
		}
	}
}

// expectSignup answers the queries a successful email sign-up makes, and
// checks how the account was inserted: with lang when one was found.
func expectSignup(mock sqlmock.Sqlmock, lang string) {
	mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(accountCols))
	args := make([]driver.Value, 12)
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	if lang != "" {
		mock.ExpectExec("insert into `accounts` \\(.*, `lang`\\) values").WithArgs(append(args, lang)...).WillReturnResult(sqlmock.NewResult(1, 1))
	} else {
		mock.ExpectExec("insert into `accounts` \\(.*`terms_accepted_at`\\) values").WithArgs(args...).WillReturnResult(sqlmock.NewResult(1, 1))
	}
	epochRow(mock, 0)
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
}

func signupRequest(body, acceptLanguage string) *http.Request {
	r := httptest.NewRequest("POST", "/api/account/signup", strings.NewReader(body))
	if acceptLanguage != "" {
		r.Header.Set("Accept-Language", acceptLanguage)
	}
	return r
}

const cSignupBody = `"email":"new@example.com","password":"long-enough-password","name":"N","surname":"S","country":"ES","accept_terms":true`

// A sign-up keeps the page's language, else the browser's, and answers it
// in the account; without one the account is created as before.
func TestSignupKeepsTheLanguage(t *testing.T) {
	for _, c := range []struct {
		body, header, want string
	}{
		{`{` + cSignupBody + `,"lang":"en"}`, "", "en"},
		{`{` + cSignupBody + `}`, "en-US,en;q=0.9", "en"},
		{`{` + cSignupBody + `}`, "", ""},
	} {
		a, mock := testAccounts(t)
		expectSignup(mock, c.want)
		w := httptest.NewRecorder()
		a.Signup(w, signupRequest(c.body, c.header))
		var out struct {
			Account struct{ Lang string }
		}
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Account.Lang != c.want {
			t.Errorf("%s / %q: %d %s", c.body, c.header, w.Code, w.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s / %q: %v", c.body, c.header, err)
		}
	}
}

// A bridge whose database has no accounts.lang yet (migration 010 not
// run) still signs people up.
func TestSignupWithoutTheLangColumn(t *testing.T) {
	a, mock := testAccounts(t)
	missing := &mysql.MySQLError{Number: 1054, Message: "Unknown column 'lang' in 'field list'"}
	mock.ExpectQuery("`lang` from `accounts` where `email` = \\?").WillReturnError(missing)
	mock.ExpectQuery("`terms_accepted_at` from `accounts` where `email` = \\?").
		WillReturnRows(sqlmock.NewRows(accountCols[:len(accountCols)-1]))
	// Known missing: the insert without it straight away.
	mock.ExpectExec("insert into `accounts` \\(.*`terms_accepted_at`\\) values").WillReturnResult(sqlmock.NewResult(1, 1))
	epochRow(mock, 0)
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
	w := httptest.NewRecorder()
	a.Signup(w, signupRequest(`{`+cSignupBody+`,"lang":"en"}`, ""))
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"lang":""`) {
		t.Fatalf("sign-up without the column: %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestSetLanguage(t *testing.T) {
	set := func(a *Accounts, body string) (int, string) {
		w := httptest.NewRecorder()
		a.SetLanguage(w, httptest.NewRequest("PUT", "/api/account/language", strings.NewReader(body)), "acc1")
		return w.Code, w.Body.String()
	}
	a, mock := testAccounts(t)
	for _, c := range []struct{ body, stored string }{
		{`{"lang":"en"}`, "en"},
		{`{"lang":"EN-gb"}`, "en"},
		{`{"lang":""}`, ""},
	} {
		mock.ExpectExec("update `accounts` set `lang` = \\? where `id` = \\?").WithArgs(c.stored, "acc1").WillReturnResult(sqlmock.NewResult(0, 1))
		code, body := set(a, c.body)
		if want := `{"lang":"` + c.stored + `","ok":true}` + "\n"; code != http.StatusOK || body != want {
			t.Errorf("%s: %d %s, want %s", c.body, code, body, want)
		}
	}
	// Refused before the database: nothing more is expected.
	for _, c := range []struct{ body, want string }{
		{`{"lang":"zz"}`, `{"code":"invalid_language","error":"that language is not available"}`},
		{`{"lang":"en; drop table accounts"}`, `{"code":"invalid_language","error":"that language is not available"}`},
		{`{}`, `{"code":"invalid_body","error":"invalid request body"}`},
		{`{"lang":3}`, `{"code":"invalid_body","error":"invalid request body"}`},
		{`not json`, `{"code":"invalid_body","error":"invalid request body"}`},
	} {
		if code, body := set(a, c.body); code != http.StatusBadRequest || body != c.want+"\n" {
			t.Errorf("%s: %d %s", c.body, code, body)
		}
	}
	// No column yet, or the database failing: try again later.
	mock.ExpectExec("update `accounts` set `lang`").WillReturnError(&mysql.MySQLError{Number: 1054, Message: "Unknown column 'lang' in 'field list'"})
	if code, body := set(a, `{"lang":"en"}`); code != http.StatusInternalServerError || !strings.Contains(body, `"save_unavailable"`) {
		t.Errorf("without the column: %d %s", code, body)
	}
	b, mock2 := testAccounts(t)
	mock2.ExpectExec("update `accounts` set `lang`").WillReturnError(errDBDown)
	if code, _ := set(b, `{"lang":"en"}`); code != http.StatusInternalServerError {
		t.Errorf("database down: %d", code)
	}
	for _, m := range []sqlmock.Sqlmock{mock, mock2} {
		if err := m.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	}
}

// The account page reads the language back from /api/account/me.
func TestMeAnswersTheLanguage(t *testing.T) {
	a, mock := testAccounts(t)
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now(), "en"))
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
	w := httptest.NewRecorder()
	a.Me(w, httptest.NewRequest("GET", "/api/account/me", nil), "acc1")
	var out struct {
		Account struct{ Lang *string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Account.Lang == nil || *out.Account.Lang != "en" {
		t.Fatalf("me: %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// An account a Google or Apple sign-in creates takes the browser's
// language too (the callback is a top-level navigation).
func TestProviderSignupKeepsTheLanguage(t *testing.T) {
	a, mock := testAccounts(t)
	newFakeIdP(t, a)
	state := strings.Repeat("ef", 32)
	expectStateConsumed(mock, state, "")
	mock.ExpectQuery("from `accounts` where `id` = \\(select `account_id` from `account_logins`").WillReturnRows(sqlmock.NewRows(accountCols))
	mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(accountCols))
	args := make([]driver.Value, 12)
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	mock.ExpectExec("insert into `accounts` \\(.*, `lang`\\) values").WithArgs(append(args, "en")...).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("insert ignore into `account_logins`").WillReturnResult(sqlmock.NewResult(1, 1))
	epochRow(mock, 0)
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))

	r := withStateCookie(callbackRequest(state, "code"), state, false)
	r.Header.Set("Accept-Language", "ja-JP, en-GB;q=0.8")
	w := httptest.NewRecorder()
	a.OAuthCallback(w, r)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/account?complete=1") {
		t.Fatalf("callback: %d %q", w.Code, w.Header().Get("Location"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
