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
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until"}).
		AddRow("acc1", "a@b.c", "A", "B", "ES", h, time.Now(), time.Now(), time.Now()))
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
