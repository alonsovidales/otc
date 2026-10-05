// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"golang.org/x/crypto/bcrypt"
)

// Issue #164: sessions carry the account's epoch; bumping it ends them.

func testAccounts(t *testing.T) (*Accounts, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Accounts{dao: dao.NewWithDB(db), secret: []byte("k"), failures: map[string][]time.Time{}}, mock
}

func epochRow(mock sqlmock.Sqlmock, epoch int) {
	mock.ExpectQuery("select `session_epoch` from `accounts`").
		WillReturnRows(sqlmock.NewRows([]string{"session_epoch"}).AddRow(epoch))
}

func withCookie(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: cSessionCookie, Value: token})
	return r
}

func TestSessionEndsWhenTheEpochMoves(t *testing.T) {
	a, mock := testAccounts(t)
	token := a.sessionToken("acc1", 3, time.Now())

	epochRow(mock, 3)
	if id, ok := a.AccountFromRequest(withCookie(httptest.NewRequest("GET", "/", nil), token)); !ok || id != "acc1" {
		t.Fatalf("a current session was refused: %q %v", id, ok)
	}
	epochRow(mock, 4) // a password change or "sign out everywhere" since
	if _, ok := a.AccountFromRequest(withCookie(httptest.NewRequest("GET", "/", nil), token)); ok {
		t.Fatal("a session from before the epoch moved still works")
	}
}

func TestOldFormatSessionIsRefused(t *testing.T) {
	a, _ := testAccounts(t)
	payload := "acc1|" + "9999999999"
	token := payload + "." + sign(a.secret, payload)
	if _, ok := a.AccountFromRequest(withCookie(httptest.NewRequest("GET", "/", nil), token)); ok {
		t.Fatal("a cookie without an epoch was accepted")
	}
}

func accountRow(mock sqlmock.Sqlmock, hash string) {
	var h any
	if hash != "" {
		h = hash
	}
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at"}).
		AddRow("acc1", "a@b.c", "A", "B", "ES", h, time.Now(), time.Now(), time.Now(), true, TermsVersion, time.Now()))
}

// verifiedRow answers the account lookup IssueSetupToken makes.
func verifiedRow(mock sqlmock.Sqlmock, id string, verified bool) {
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at"}).
		AddRow(id, "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), verified, TermsVersion, time.Now()))
}

// No setup code - so no device name - for an email nobody proved.
func TestSetupTokenNeedsAVerifiedEmail(t *testing.T) {
	a, mock := testAccounts(t)
	verifiedRow(mock, "acc1", false)
	if _, err := a.IssueSetupToken("acc1"); err != ErrEmailNotVerified {
		t.Fatalf("an unverified account got a setup code: %v", err)
	}
	verifiedRow(mock, "acc1", true)
	mock.ExpectExec("insert into `account_tokens`").WillReturnResult(sqlmock.NewResult(1, 1))
	if _, err := a.IssueSetupToken("acc1"); err != nil {
		t.Fatalf("a verified account got no setup code: %v", err)
	}
	// The database failing is not "confirm your email first".
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnError(errDBDown)
	if _, err := a.IssueSetupToken("acc1"); err == nil || err == ErrEmailNotVerified {
		t.Fatalf("a database error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A wizard checking a setup code while the database fails hears "try
// again", not that the code is not valid.
func TestSetupTokenInfoOnADatabaseError(t *testing.T) {
	info := func(a *Accounts) int {
		w := httptest.NewRecorder()
		a.SetupTokenInfo(w, httptest.NewRequest("GET", "/api/account/setup-token-info?token=ABCD-EFGH", nil))
		return w.Code
	}
	tokenRow := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("select `account_id` from `account_tokens`").WithArgs("ABCDEFGH", cPurposeSetup, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow("acc1"))
	}
	a, mock := testAccounts(t)
	mock.ExpectQuery("select `account_id` from `account_tokens`").WillReturnError(errDBDown)
	if code := info(a); code != http.StatusInternalServerError {
		t.Errorf("token lookup failing: %d", code)
	}
	tokenRow(mock)
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnError(errDBDown)
	if code := info(a); code != http.StatusInternalServerError {
		t.Errorf("account lookup failing: %d", code)
	}
	tokenRow(mock)
	verifiedRow(mock, "acc1", false)
	if code := info(a); code != http.StatusNotFound {
		t.Errorf("an unverified account's code: %d", code)
	}
	tokenRow(mock)
	verifiedRow(mock, "acc1", true)
	verifiedRow(mock, "acc1", true)
	if code := info(a); code != http.StatusOK {
		t.Errorf("a good code: %d", code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Issue #175: no setup code until the terms in force are accepted - none
// recorded (an account from before), or an older version.
func TestSetupTokenNeedsTheTerms(t *testing.T) {
	a, mock := testAccounts(t)
	cols := []string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at"}
	for _, version := range []any{nil, "2020-01-01"} {
		mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(cols).
			AddRow("acc1", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), true, version, nil))
		if _, err := a.IssueSetupToken("acc1"); err != ErrTermsNotAccepted {
			t.Fatalf("terms %v: got %v, want ErrTermsNotAccepted", version, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A verification link works once.
func TestVerifyLinkIsSingleUse(t *testing.T) {
	a, mock := testAccounts(t)
	h := hashEmailToken("tok")
	mock.ExpectBegin()
	mock.ExpectQuery("from `account_email_tokens`").WithArgs(h, cPurposeVerify, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow("acc1"))
	mock.ExpectExec("delete from `account_email_tokens`").WithArgs(h).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec("update `accounts` set `email_verified` = 1").WithArgs("acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	w := httptest.NewRecorder()
	a.Verify(w, httptest.NewRequest("POST", "/api/account/verify", strings.NewReader(`{"token":"tok"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("a valid link: %d %s", w.Code, w.Body)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("from `account_email_tokens`").WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectRollback()
	w = httptest.NewRecorder()
	a.Verify(w, httptest.NewRequest("POST", "/api/account/verify", strings.NewReader(`{"token":"tok"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a used link: %d", w.Code)
	}
}

// "Forgot my password" answers the same for an email with no account.
func TestForgotDoesNotRevealAccounts(t *testing.T) {
	a, mock := testAccounts(t)
	mock.ExpectQuery("from `accounts` where `email` = \\?").WillReturnRows(sqlmock.NewRows(
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at"}))
	w := httptest.NewRecorder()
	a.Forgot(w, httptest.NewRequest("POST", "/api/account/forgot", strings.NewReader(`{"email":"nobody@example.com"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("unknown email: %d", w.Code)
	}
}

func TestChangingThePasswordNeedsTheCurrentOne(t *testing.T) {
	a, mock := testAccounts(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)

	accountRow(mock, string(hash))
	w := httptest.NewRecorder()
	a.SetPassword(w, httptest.NewRequest("PUT", "/api/account/password", strings.NewReader(`{"password":"new-password","current":"wrong"}`)), "acc1")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password: %d, want 401", w.Code)
	}

	// The password and the epoch move in one statement, which also hands
	// back the new epoch (LAST_INSERT_ID).
	accountRow(mock, string(hash))
	mock.ExpectExec(cSetPasswordEndingSessions).WillReturnResult(sqlmock.NewResult(1, 1))
	w = httptest.NewRecorder()
	a.SetPassword(w, httptest.NewRequest("PUT", "/api/account/password", strings.NewReader(`{"password":"new-password","current":"old-password"}`)), "acc1")
	if w.Code != http.StatusOK {
		t.Fatalf("right current password: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), cSessionCookie+"=acc1|1|") {
		t.Errorf("this session didn't get a cookie for the new epoch: %q", w.Header().Get("Set-Cookie"))
	}

	// That write failing changed nothing: an honest 500, no cookie.
	accountRow(mock, string(hash))
	mock.ExpectExec(cSetPasswordEndingSessions).WillReturnError(errDBDown)
	w = httptest.NewRecorder()
	a.SetPassword(w, httptest.NewRequest("PUT", "/api/account/password", strings.NewReader(`{"password":"new-password","current":"old-password"}`)), "acc1")
	if w.Code != http.StatusInternalServerError || w.Header().Get("Set-Cookie") != "" {
		t.Errorf("a failed change: %d, cookie %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

const cSetPasswordEndingSessions = "update `accounts` set `password_hash` = \\?, `session_epoch` = LAST_INSERT_ID\\(`session_epoch` \\+ 1\\)"

// A reset sets the password, proves the email and ends every other
// session; this browser's new cookie carries the new epoch. When the write
// fails it says so, instead of an ok that leaves the old sessions alive.
func TestResetEndsTheOtherSessions(t *testing.T) {
	expectToken := func(mock sqlmock.Sqlmock) {
		mock.ExpectBegin()
		mock.ExpectQuery("from `account_email_tokens`").WithArgs(hashEmailToken("tok"), cPurposeReset, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow("acc1"))
		mock.ExpectExec("delete from `account_email_tokens`").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}
	reset := func(a *Accounts) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.Reset(w, httptest.NewRequest("POST", "/api/account/reset", strings.NewReader(`{"token":"tok","password":"new-password"}`)))
		return w
	}

	a, mock := testAccounts(t)
	expectToken(mock)
	mock.ExpectExec(cSetPasswordEndingSessions).WithArgs(sqlmock.AnyArg(), "acc1").WillReturnResult(sqlmock.NewResult(5, 1))
	mock.ExpectExec("update `accounts` set `email_verified` = 1").WithArgs("acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	w := reset(a)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Set-Cookie"), cSessionCookie+"=acc1|5|") {
		t.Errorf("reset: %d, cookie %q", w.Code, w.Header().Get("Set-Cookie"))
	}

	expectToken(mock)
	mock.ExpectExec(cSetPasswordEndingSessions).WillReturnError(errDBDown)
	w = reset(a)
	if w.Code != http.StatusInternalServerError || w.Header().Get("Set-Cookie") != "" {
		t.Errorf("a failed reset: %d, cookie %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// An account without a password (Google/Apple) may set one only right
// after signing in.
func TestFirstPasswordNeedsAFreshSignIn(t *testing.T) {
	a, mock := testAccounts(t)
	old := a.sessionToken("acc1", 0, time.Now().Add(-time.Hour))
	accountRow(mock, "")
	epochRow(mock, 0)
	w := httptest.NewRecorder()
	a.SetPassword(w, withCookie(httptest.NewRequest("PUT", "/api/account/password", strings.NewReader(`{"password":"new-password"}`)), old), "acc1")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an hour-old sign-in set a first password: %d", w.Code)
	}
}

// Saving the profile is no sign-in: it must not give an old session a new
// cookie, which would pass the 15-minute check for a first password or for
// deleting the account.
func TestSavingTheProfileKeepsTheSessionAge(t *testing.T) {
	a, mock := testAccounts(t)
	accountRow(mock, "")
	mock.ExpectExec("update `accounts` set `name` = \\?").WillReturnResult(sqlmock.NewResult(0, 1))
	accountRow(mock, "")
	mock.ExpectExec("update `accounts` set `last_seen`").WillReturnResult(sqlmock.NewResult(0, 1))
	w := httptest.NewRecorder()
	a.UpdateProfile(w, httptest.NewRequest("PUT", "/api/account/me", strings.NewReader(`{"name":"A","surname":"B","country":"ES"}`)), "acc1")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"account"`) {
		t.Fatalf("profile saved: %d %s", w.Code, w.Body)
	}
	if c := w.Header().Get("Set-Cookie"); c != "" {
		t.Errorf("saving the profile issued a session cookie: %q", c)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The login limiter keeps entries only for addresses with recent
// failures: a caller that never failed (or failed long ago) leaves none,
// and a sweep keeps an address that is locked out right now.
func TestLoginLimiterKeepsOnlyRecentFailures(t *testing.T) {
	a, _ := testAccounts(t)
	now := time.Now()
	if !a.loginAllowed("198.51.100.1", now) || len(a.failures) != 0 {
		t.Fatalf("an address with no failures left an entry: %v", a.failures)
	}
	a.failures["198.51.100.2"] = []time.Time{now.Add(-time.Hour), now.Add(-30 * time.Minute)}
	if !a.loginAllowed("198.51.100.2", now) || len(a.failures) != 0 {
		t.Fatalf("stale failures left an entry: %v", a.failures)
	}

	for i := 0; i < cLoginFailures; i++ {
		a.loginFailed("198.51.100.3", now)
	}
	a.failures["198.51.100.4"] = []time.Time{now.Add(-2 * cLoginWindow)}
	a.pruneFailuresLocked(now)
	if _, ok := a.failures["198.51.100.4"]; ok {
		t.Error("the sweep kept an address whose failures are all stale")
	}
	if a.loginAllowed("198.51.100.3", now) {
		t.Error("the sweep lifted a lockout in force")
	}

	// Past the cap, stale entries go before anyone's lockout does.
	for i := 0; i < 10000; i++ {
		a.failures[fmt.Sprintf("stale-%d", i)] = []time.Time{now.Add(-time.Hour)}
	}
	a.loginFailed("198.51.100.5", now)
	if a.loginAllowed("198.51.100.3", now) || len(a.failures) != 2 {
		t.Errorf("the cap dropped a lockout in force (%d entries left)", len(a.failures))
	}
}
