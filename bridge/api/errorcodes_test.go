// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/accounts"
	"github.com/alonsovidales/otc/bridge/dao"
)

// Every JSON error answers a stable code next to the English text the
// older clients show: {"code","error"}, keys in that order (encoding/json
// sorts them), the text unchanged.
func TestErrorAnswersCarryACode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONErr(rec, http.StatusConflict, "name_taken", "that name is already taken")
	if rec.Code != http.StatusConflict || rec.Header().Get("Content-Type") != "application/json" ||
		rec.Body.String() != `{"code":"name_taken","error":"that name is already taken"}`+"\n" {
		t.Errorf("got %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	// The two codes from before, byte for byte what they were.
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)
	api := &API{muxHTTPServer: http.NewServeMux(), dao: d, accounts: accounts.Init(d, []byte("test session secret"), "off-the.cloud"), lastClaimByAddr: map[string]time.Time{}}
	req := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(
		`{"name":"newpi","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`))
	rec = httptest.NewRecorder()
	api.claimName(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != `{"code":"login_required","error":"sign in to register a name"}`+"\n" {
		t.Errorf("claim without an account: %d %s", rec.Code, rec.Body.String())
	}

	mock.ExpectQuery("select count\\(\\*\\) from `devices` where `account_id` = \\?").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(accounts.MaxDomains))
	if status, e := api.domainLimitReached("acc1"); status != http.StatusForbidden || e.code != "domain_limit" ||
		e.msg != "an account can register up to 5 domains - for more (2 euros a year each), write to info@off-the.cloud" {
		t.Errorf("at the limit: %d %+v", status, e)
	}
	// A count that can't be read is "try again", not the limit.
	mock.ExpectQuery("select count\\(\\*\\) from `devices` where `account_id` = \\?").WillReturnError(errors.New("driver: bad connection"))
	if status, e := api.domainLimitReached("acc1"); status != http.StatusInternalServerError || e.code != "account_check_unavailable" {
		t.Errorf("count failed: %d %+v", status, e)
	}
	if status, _ := func() (int, apiError) {
		mock.ExpectQuery("select count\\(\\*\\) from `devices` where `account_id` = \\?").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		return api.domainLimitReached("acc1")
	}(); status != 0 {
		t.Errorf("under the limit: %d", status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The account page's refusals for a claim carry their codes too.
func TestClaimRefusalsCarryTheirCodes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := []byte("test session secret")
	d := dao.NewWithDB(db)
	api := &API{muxHTTPServer: http.NewServeMux(), dao: d, accounts: accounts.Init(d, secret, "off-the.cloud"), lastClaimByAddr: map[string]time.Time{}}
	claim := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(
			`{"name":"newpi","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`))
		req.AddCookie(sessionCookie(secret, "acc1"))
		rec := httptest.NewRecorder()
		api.claimName(rec, req)
		return rec
	}
	for _, c := range []struct {
		verified bool
		terms    string
		want     string
	}{
		{false, accounts.TermsVersion, `{"code":"email_not_verified","error":"confirm your email first - open the link we sent you, or ask for a new one above"}`},
		{true, "2020-01-01", `{"code":"terms_not_accepted","error":"accept the terms of use above first"}`},
	} {
		mock.ExpectQuery("select `session_epoch` from `accounts`").WillReturnRows(sqlmock.NewRows([]string{"session_epoch"}).AddRow(0))
		accountRow(mock, c.verified, c.terms)
		if rec := claim(); rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != c.want {
			t.Errorf("verified %v, terms %s: %d %s", c.verified, c.terms, rec.Code, rec.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// goErrors is every error the Go side of the account API writes: code ->
// the English texts written with it (none for a text made at run time).
// Read from the sources of this package and bridge/accounts, so a code
// added there is seen here without a list to keep.
func goErrors(t *testing.T) map[string][]string {
	t.Helper()
	var files []string
	for _, dir := range []string{".", "../accounts"} {
		m, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range m {
			if !strings.HasSuffix(f, "_test.go") {
				files = append(files, f)
			}
		}
	}
	quoted := `"(?:[^"\\]|\\.)*"|` + "`[^`]*`"
	calls := regexp.MustCompile(`(?:writeError|writeJSONErr)\(w, [^,]+, "([^"]*)", (` + quoted + `|\w+)`)
	literals := regexp.MustCompile(`apiError\{"([^"]*)", (` + quoted + `|\w+)`)
	codeOnly := regexp.MustCompile(`\["code"\] = "([^"]*)"`)
	consts := regexp.MustCompile(`(?m)^\s*(?:const\s+)?(c\w+)\s*=\s*(` + quoted + `)\s*$`)

	var src strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
		src.WriteByte('\n')
	}
	all := src.String()
	named := map[string]string{}
	for _, m := range consts.FindAllStringSubmatch(all, -1) {
		if s, err := strconv.Unquote(m[2]); err == nil {
			named[m[1]] = s
		}
	}
	out := map[string][]string{}
	add := func(code, expr string) {
		if _, ok := out[code]; !ok {
			out[code] = nil
		}
		text, err := strconv.Unquote(expr)
		if err != nil {
			text = named[expr] // a constant; "" for anything made at run time
		}
		if text != "" && !slices.Contains(out[code], text) {
			out[code] = append(out[code], text)
		}
	}
	for _, m := range calls.FindAllStringSubmatch(all, -1) {
		add(m[1], m[2])
	}
	for _, m := range literals.FindAllStringSubmatch(all, -1) {
		add(m[1], m[2])
	}
	for _, m := range codeOnly.FindAllStringSubmatch(all, -1) {
		add(m[1], "")
	}
	if len(out) < 40 {
		t.Fatalf("found only %d codes in the sources: the patterns no longer match the code", len(out))
	}
	return out
}

var cCodePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// pageErrorTable is account.html's #error-text table.
func pageErrorTable(t *testing.T) (api, signIn map[string]string) {
	t.Helper()
	b, err := os.ReadFile("../static/account.html")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), `<script type="application/json" id="error-text">`)
	body, _, ok2 := strings.Cut(rest, "</script>")
	if !ok || !ok2 {
		t.Fatal("account.html has no #error-text table")
	}
	var table struct {
		API    map[string]string `json:"api"`
		SignIn map[string]string `json:"signin"`
	}
	if err := json.Unmarshal([]byte(body), &table); err != nil {
		t.Fatalf("account.html's #error-text is not JSON: %v", err)
	}
	return table.API, table.SignIn
}

// The account page's English for each code is today's server text, so the
// page reads as before; a code the page lists is one the bridge sends.
// Where one code has several server wordings the page takes one of them,
// except recent_signin_required, worded for both the password and the
// deletion it guards.
func TestAccountPageErrorTable(t *testing.T) {
	codes := goErrors(t)
	for code := range codes {
		if !cCodePattern.MatchString(code) {
			t.Errorf("code %q is not snake_case", code)
		}
	}
	page, _ := pageErrorTable(t)
	if len(page) == 0 {
		t.Fatal("account.html's table has no api codes")
	}
	// The run-time texts, from the code that makes them.
	dynamic := map[string]string{
		"domain_limit": func() string {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("select count").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(accounts.MaxDomains))
			_, e := (&API{dao: dao.NewWithDB(db)}).domainLimitReached("acc1")
			return e.msg
		}(),
		"password_too_short": func() string {
			db, _, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rec := httptest.NewRecorder()
			accounts.Init(dao.NewWithDB(db), []byte("k"), "off-the.cloud").Signup(rec, httptest.NewRequest(http.MethodPost, "/api/account/signup",
				strings.NewReader(`{"email":"a@b.c","password":"short"}`)))
			var out struct{ Code, Error string }
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if out.Code != "password_too_short" {
				t.Fatalf("a short password: %d %s", rec.Code, rec.Body.String())
			}
			return out.Error
		}(),
	}
	reworded := map[string]bool{"recent_signin_required": true}
	fill := strings.NewReplacer("{min_password}", "8", "{max_domains}", strconv.Itoa(accounts.MaxDomains),
		"{extra_domain_eur}", strconv.Itoa(accounts.ExtraDomainEUR), "{contact}", accounts.ContactEmail)
	for code, text := range page {
		texts, ok := codes[code]
		if !ok {
			t.Errorf("account.html lists %q, which the bridge never sends", code)
			continue
		}
		if s, ok := dynamic[code]; ok {
			texts = append(texts, s)
		}
		if reworded[code] || len(texts) == 0 {
			continue
		}
		got, found := fill.Replace(text), false
		for _, s := range texts {
			found = found || s == got
		}
		if !found {
			t.Errorf("account.html says %q for %s, the bridge %q", got, code, texts)
		}
	}
}
