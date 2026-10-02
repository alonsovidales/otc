// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
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
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified"}).
		AddRow("acc1", "a@b.c", "A", "B", "ES", h, time.Now(), time.Now(), time.Now(), true))
}

// verifiedRow answers the account lookup IssueSetupToken makes.
func verifiedRow(mock sqlmock.Sqlmock, id string, verified bool) {
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified"}).
		AddRow(id, "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), verified))
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
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified"}))
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

	accountRow(mock, string(hash))
	mock.ExpectExec("update `accounts` set `password_hash`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `accounts` set `session_epoch` = `session_epoch` \\+ 1").WillReturnResult(sqlmock.NewResult(0, 1))
	epochRow(mock, 1) // read back by the bump
	epochRow(mock, 1) // the new cookie for this session
	w = httptest.NewRecorder()
	a.SetPassword(w, httptest.NewRequest("PUT", "/api/account/password", strings.NewReader(`{"password":"new-password","current":"old-password"}`)), "acc1")
	if w.Code != http.StatusOK {
		t.Fatalf("right current password: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), cSessionCookie+"=acc1|1|") {
		t.Errorf("this session didn't get a cookie for the new epoch: %q", w.Header().Get("Set-Cookie"))
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
