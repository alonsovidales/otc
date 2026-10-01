// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAppReturnNeedsAChallenge(t *testing.T) {
	sum := sha256.Sum256([]byte("verifier-verifier-verifier-verifier-verifier"))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if _, ok := appReturnWithChallenge(challenge); !ok {
		t.Fatal("a real S256 challenge was refused")
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 44), strings.Repeat("!", 43)} {
		if _, ok := appReturnWithChallenge(bad); ok {
			t.Errorf("challenge %q accepted", bad)
		}
	}
	if !verifierMatches("verifier-verifier-verifier-verifier-verifier", challenge) || verifierMatches("other", challenge) {
		t.Fatal("verifier check wrong")
	}
	for raw, want := range map[string]bool{
		"otcsetup://done":                        true,
		"otcsetup://done?challenge=" + challenge: true,
		"otcsetup://evil":                        false,
		"otcsetup://done?challenge=x":            false,
		"https://evil.example/":                  false,
		"http://192.168.1.10/":                   true,
	} {
		if _, ok := validReturnURL(raw); ok != want {
			t.Errorf("validReturnURL(%q) = %v, want %v", raw, ok, want)
		}
	}
}

// The redirect to the app carries only a one-time code, never the token;
// the code is kept in the database (by its hash), so the exchange works
// on any bridge node (issue #144), and only once.
func TestAppCodeAcrossNodes(t *testing.T) {
	sum := sha256.Sum256([]byte("verifier-verifier-verifier-verifier-verifier"))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	ret, _ := appReturnWithChallenge(challenge)

	// Node A: Apple's callback.
	nodeA, mockA := testAccounts(t)
	mockA.ExpectExec("delete from `app_signin_codes` where `created` <").WillReturnResult(sqlmock.NewResult(0, 0))
	var stored string
	mockA.ExpectExec("insert into `app_signin_codes`").WithArgs(hashCapture{&stored}, "acc-1", challenge, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	dest := nodeA.appRedirect("acc-1", ret)
	u, err := url.Parse(dest)
	code := u.Query().Get("code")
	if err != nil || !isAppReturn(u) || code == "" || u.Query().Get("setup_token") != "" {
		t.Fatalf("redirect %q", dest)
	}
	if stored != codeHash(code) || stored == code {
		t.Fatal("the code itself was stored, not its hash")
	}

	// Node B: the app's exchange.
	nodeB, mockB := testAccounts(t)
	mockB.ExpectBegin()
	mockB.ExpectQuery("select `account_id`, `challenge`, `created` from `app_signin_codes`").WithArgs(stored).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "challenge", "created"}).AddRow("acc-1", challenge, time.Now().UTC()))
	mockB.ExpectExec("delete from `app_signin_codes`").WithArgs(stored).WillReturnResult(sqlmock.NewResult(0, 1))
	mockB.ExpectCommit()
	mockB.ExpectExec("insert into `account_tokens`").WillReturnResult(sqlmock.NewResult(1, 1))
	w := httptest.NewRecorder()
	nodeB.AppExchange(w, httptest.NewRequest("POST", "/api/account/app-exchange",
		strings.NewReader(`{"code":"`+code+`","verifier":"verifier-verifier-verifier-verifier-verifier"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "setup_token") {
		t.Fatalf("exchange on the other node: %d %s", w.Code, w.Body)
	}

	// The same code again: gone.
	mockB.ExpectBegin()
	mockB.ExpectQuery("from `app_signin_codes`").WillReturnRows(sqlmock.NewRows([]string{"account_id", "challenge", "created"}))
	mockB.ExpectRollback()
	w = httptest.NewRecorder()
	nodeB.AppExchange(w, httptest.NewRequest("POST", "/api/account/app-exchange",
		strings.NewReader(`{"code":"`+code+`","verifier":"verifier-verifier-verifier-verifier-verifier"}`)))
	if w.Code != 401 {
		t.Fatalf("a used code: %d, want 401", w.Code)
	}
	if err := mockB.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A wrong verifier is refused (PKCE), and the code is spent anyway.
func TestAppCodeNeedsTheVerifier(t *testing.T) {
	sum := sha256.Sum256([]byte("verifier-verifier-verifier-verifier-verifier"))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	a, mock := testAccounts(t)
	mock.ExpectBegin()
	mock.ExpectQuery("from `app_signin_codes`").WillReturnRows(sqlmock.NewRows([]string{"account_id", "challenge", "created"}).AddRow("acc-1", challenge, time.Now().UTC()))
	mock.ExpectExec("delete from `app_signin_codes`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	w := httptest.NewRecorder()
	a.AppExchange(w, httptest.NewRequest("POST", "/api/account/app-exchange", strings.NewReader(`{"code":"c","verifier":"someone-else"}`)))
	if w.Code != 401 {
		t.Fatalf("wrong verifier: %d, want 401", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// hashCapture records the argument it is matched against.
type hashCapture struct{ to *string }

func (h hashCapture) Match(v driver.Value) bool {
	s, ok := v.(string)
	*h.to = s
	return ok && len(s) == 64
}
