// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/limits"
	"github.com/alonsovidales/otc/bridge/site"
	"golang.org/x/crypto/bcrypt"
)

// Every JSON error answers {"code","error"}: a stable code next to the
// English text the older clients show, unchanged.
func TestErrorAnswersCarryACode(t *testing.T) {
	a, _ := testAccounts(t)
	w := httptest.NewRecorder()
	a.RequireAuth(func(http.ResponseWriter, *http.Request, string) {
		t.Error("the handler ran without a session")
	})(w, httptest.NewRequest("GET", "/api/account/me", nil))
	if w.Code != http.StatusUnauthorized || w.Header().Get("Content-Type") != "application/json" ||
		w.Body.String() != `{"code":"not_signed_in","error":"not signed in"}`+"\n" {
		t.Errorf("got %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}

// A failed sign-in has one answer, code included, whether the email has
// no account, the password is wrong, or the account has no password
// (Google/Apple): the code tells no more than the text did (issue #163).
func TestLoginAnswersOneCodeForAnyFailure(t *testing.T) {
	a, mock := testAccounts(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("right-password"), bcrypt.MinCost)
	byEmail := "from `accounts` where `email` = \\?"
	mock.ExpectQuery(byEmail).WillReturnRows(sqlmock.NewRows(accountCols))
	mock.ExpectQuery(byEmail).WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", "ES", string(hash), time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now(), ""))
	mock.ExpectQuery(byEmail).WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc2", "c@d.e", "C", "D", "ES", nil, time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now(), ""))
	want := `{"code":"invalid_credentials","error":"wrong email or password - if you signed up with Google or Apple, use that instead (on the account page, then a setup code)"}` + "\n"
	for _, body := range []string{
		`{"email":"nobody@example.com","password":"right-password"}`,
		`{"email":"a@b.c","password":"wrong-password"}`,
		`{"email":"c@d.e","password":"right-password"}`,
	} {
		w := httptest.NewRecorder()
		a.Login(w, httptest.NewRequest("POST", "/api/account/login", strings.NewReader(body)))
		if w.Code != http.StatusUnauthorized || w.Body.String() != want {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// "Forgot my password" answers {"ok":true} and nothing else, for an email
// with no account and when the lookup fails alike.
func TestForgotAnswersOnlyOk(t *testing.T) {
	a, mock := testAccounts(t)
	mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(accountCols))
	mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnError(errDBDown)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		a.Forgot(w, httptest.NewRequest("POST", "/api/account/forgot", strings.NewReader(`{"email":"nobody@example.com"}`)))
		if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}`+"\n" {
			t.Errorf("attempt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A wizard signing in an account whose email isn't confirmed gets
// verify_email, the English text older wizards show, and now a code.
func TestSetupSignInOfAnUnverifiedAccount(t *testing.T) {
	a, mock := testAccounts(t)
	// At the current cost, so the sign-in doesn't rehash it.
	hash, err := bcrypt.GenerateFromPassword([]byte("right-password"), limits.BcryptCost)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc1", "a@b.c", "A", "B", "ES", string(hash), time.Now(), time.Now(), time.Now(), false, TermsVersion, time.Now(), ""))
	epochRow(mock, 0)
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
	w := httptest.NewRecorder()
	a.Login(w, httptest.NewRequest("POST", "/api/account/login?for=setup", strings.NewReader(`{"email":"a@b.c","password":"right-password"}`)))
	var out struct {
		Code, Error string
		VerifyEmail bool `json:"verify_email"`
		SetupToken  string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusForbidden || out.Code != "email_not_verified" || !out.VerifyEmail || out.SetupToken != "" ||
		out.Error != "Confirm your email first: we sent a link to a@b.c. Open it, then sign in here again to continue." {
		t.Errorf("got %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A provider sign-in that went wrong comes back to the account page with
// a code in ?error=, never text: not the provider's own error parameter,
// nor words of the bridge's that a crafted link could imitate.
func TestSignInErrorRedirectsCarryACode(t *testing.T) {
	state := strings.Repeat("ab", 32)
	callback := func(query string) *http.Request {
		r := httptest.NewRequest("GET", "/account/auth/google/callback?"+query, nil)
		r.SetPathValue("provider", "google")
		return r
	}
	location := func(a *Accounts, r *http.Request) string {
		w := httptest.NewRecorder()
		a.OAuthCallback(w, r)
		if w.Code != http.StatusFound || sessionCookie(w) != "" {
			t.Errorf("%s: %d, session %q", r.URL, w.Code, sessionCookie(w))
		}
		return w.Header().Get("Location")
	}

	a, mock := testAccounts(t)
	f := newFakeIdP(t, a)
	for _, q := range []string{
		"error=" + url.QueryEscape("<b>the bridge is down, sign in at evil.example</b>") + "&state=" + state,
		"error=access_denied&state=" + state + "&code=code",
		"state=" + state, // no code
	} {
		if loc := location(a, withStateCookie(callback(q), state, false)); loc != "/account?error=cancelled" {
			t.Errorf("%s: sent to %q", q, loc)
		}
	}
	if loc := location(a, callbackRequest(state, "code")); loc != "/account?error=expired" {
		t.Errorf("a sign-in this browser didn't start: sent to %q", loc)
	}

	// The provider answered, but without an email it verified.
	f.claims["email_verified"] = false
	expectStateConsumed(mock, state, "")
	if loc := location(a, withStateCookie(callbackRequest(state, "code"), state, false)); loc != "/account?error=no_email" {
		t.Errorf("no verified email: sent to %q", loc)
	}
	// The code exchange itself failed.
	a.providers["google"].tokenURL = "http://127.0.0.1:0/token"
	expectStateConsumed(mock, state, "")
	if loc := location(a, withStateCookie(callbackRequest(state, "code"), state, false)); loc != "/account?error=failed" {
		t.Errorf("a failed exchange: sent to %q", loc)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The account page has its own message for every sign-in code, so none
// of them falls to its general one; the page never shows ?error= itself.
func TestAccountPageKnowsEverySignInCode(t *testing.T) {
	b, err := site.Render(site.Account, site.English(), site.Options{Base: "https://off-the.cloud", Nonce: "n", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), `<script type="application/json" id="error-text">`)
	body, _, ok2 := strings.Cut(rest, "</script>")
	if !ok || !ok2 {
		t.Fatal("account.html has no #error-text table")
	}
	var table struct {
		SignIn map[string]string `json:"signin"`
	}
	if err := json.Unmarshal([]byte(body), &table); err != nil {
		t.Fatal(err)
	}
	var got []string
	for code, text := range table.SignIn {
		got = append(got, code)
		if strings.TrimSpace(text) == "" {
			t.Errorf("no message for %q", code)
		}
	}
	slices.Sort(got)
	want := []string{cSignInCancelled, cSignInExpired, cSignInFailed, cSignInNoEmail}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("account.html knows %v, the callback sends %v", got, want)
	}
	if strings.Contains(string(b), "banner(params.get('error'))") {
		t.Error("account.html shows ?error= as it is")
	}
}

// A sign-up or a profile whose name, surname or country won't do says
// which, each field with its own code - in "fields", next to the code and
// English every client from before goes by, both unchanged.
func TestProfileAnswersSayWhichFieldIsWrong(t *testing.T) {
	long := strings.Repeat("x", cNameMax+1)
	cases := []struct {
		name, surname, country string
		want                   map[string]string
	}{
		{"", "B", "ES", map[string]string{"name": "name_required"}},
		{"  ", "B", "ES", map[string]string{"name": "name_required"}},
		{"A", "", "ES", map[string]string{"surname": "surname_required"}},
		{"A", long, "ES", map[string]string{"surname": "surname_invalid"}},
		{"<b>Ann</b>", "B", "", map[string]string{"name": "name_invalid", "country": "country_required"}},
		{"A", "B", "XX", map[string]string{"country": "country_invalid"}},
		{"", "", "", map[string]string{"name": "name_required", "surname": "surname_required", "country": "country_required"}},
	}
	check := func(what string, w *httptest.ResponseRecorder, want map[string]string) {
		t.Helper()
		var out struct {
			Code, Error string
			Fields      map[string]string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusBadRequest || out.Code != "profile_incomplete" || out.Error != "name, surname and country of residence are required" ||
			!maps.Equal(out.Fields, want) {
			t.Errorf("%s: %d %s, want fields %v", what, w.Code, w.Body.String(), want)
		}
	}
	for _, c := range cases {
		a, _ := testAccounts(t)
		body, _ := json.Marshal(map[string]any{"email": "a@b.c", "password": "long-enough", "name": c.name, "surname": c.surname, "country": c.country, "accept_terms": true})
		w := httptest.NewRecorder()
		a.Signup(w, httptest.NewRequest("POST", "/api/account/signup", strings.NewReader(string(body))))
		check("sign-up "+string(body), w, c.want)

		a, mock := testAccounts(t)
		accountRow(mock, "") // terms accepted: straight to the fields
		body, _ = json.Marshal(map[string]any{"name": c.name, "surname": c.surname, "country": c.country})
		w = httptest.NewRecorder()
		a.UpdateProfile(w, httptest.NewRequest("PUT", "/api/account/me", strings.NewReader(string(body))), "acc1")
		check("profile "+string(body), w, c.want)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	}
	// Byte for byte, keys in order: what a client from before reads first.
	a, _ := testAccounts(t)
	w := httptest.NewRecorder()
	a.Signup(w, httptest.NewRequest("POST", "/api/account/signup", strings.NewReader(`{"email":"a@b.c","password":"long-enough","name":"A","surname":"B"}`)))
	if want := `{"code":"profile_incomplete","error":"name, surname and country of residence are required","fields":{"country":"country_required"}}` + "\n"; w.Body.String() != want {
		t.Errorf("got %s, want %s", w.Body.String(), want)
	}
}

// The account page has words for every per-field code and for every code
// the bridge answers a 429 with that says when to come back.
func TestAccountPageKnowsEveryFieldAndWaitCode(t *testing.T) {
	b, err := site.Render(site.Account, site.English(), site.Options{Base: "https://off-the.cloud", Nonce: "n", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(string(b), `<script type="application/json" id="error-text">`)
	body, _, _ := strings.Cut(rest, "</script>")
	var table struct {
		Wait   map[string]string `json:"wait"`
		Fields map[string]string `json:"fields"`
		Retry  []string          `json:"retry"`
	}
	if err := json.Unmarshal([]byte(body), &table); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, codes := range profileFieldCodes {
		want = append(want, codes.required, codes.invalid)
	}
	if got := slices.Sorted(maps.Keys(table.Fields)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("account.html words the field codes %v, the bridge sends %v", got, want)
	}
	if got := slices.Sorted(maps.Keys(table.Wait)); !slices.Equal(got, []string{"too_many_attempts", "too_many_emails", "too_many_signups"}) {
		t.Errorf("account.html words the waits of %v", got)
	}
	if len(table.Retry) != 61 || table.Retry[1] != "Try again in 1 minute." || table.Retry[4] != "Try again in 4 minutes." {
		t.Errorf("retry: %d texts, %q", len(table.Retry), table.Retry)
	}
}
