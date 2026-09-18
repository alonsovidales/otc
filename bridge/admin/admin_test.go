// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
)

func TestSessionTokenRoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()

	token := newSessionToken(secret, "alice", now)

	username, ok := verifySessionToken(secret, token, now)
	if !ok {
		t.Fatal("expected a freshly issued token to verify")
	}
	if username != "alice" {
		t.Errorf("username = %q, want %q", username, "alice")
	}
}

func TestSessionTokenExpires(t *testing.T) {
	secret := []byte("test-secret")
	issued := time.Now()

	token := newSessionToken(secret, "alice", issued)

	if _, ok := verifySessionToken(secret, token, issued.Add(cSessionTTL-time.Minute)); !ok {
		t.Error("expected the token to still be valid just before its TTL elapses")
	}
	if _, ok := verifySessionToken(secret, token, issued.Add(cSessionTTL+time.Minute)); ok {
		t.Error("expected the token to be rejected once its TTL has elapsed")
	}
}

func TestSessionTokenRejectsTamperedPayload(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()

	token := newSessionToken(secret, "alice", now)
	// Swap the username but keep the original signature.
	forged := "bob|" + token[len("alice|"):]

	if _, ok := verifySessionToken(secret, forged, now); ok {
		t.Error("expected a token with a tampered payload to fail verification")
	}
}

func TestSessionTokenRejectsWrongSecret(t *testing.T) {
	now := time.Now()
	token := newSessionToken([]byte("secret-a"), "alice", now)

	if _, ok := verifySessionToken([]byte("secret-b"), token, now); ok {
		t.Error("expected a token signed with a different secret to fail verification")
	}
}

func TestSessionTokenRejectsMalformedInput(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()

	cases := []string{
		"",
		"no-dot-separator",
		"payload-with-no-pipe." + signToken(secret, "payload-with-no-pipe"),
		"alice|not-a-number." + signToken(secret, "alice|not-a-number"),
	}

	for _, tc := range cases {
		if _, ok := verifySessionToken(secret, tc, now); ok {
			t.Errorf("expected malformed token %q to fail verification", tc)
		}
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}

	if !checkPassword(hash, "correct horse battery staple") {
		t.Error("expected the correct password to check out against its own hash")
	}
	if checkPassword(hash, "wrong password") {
		t.Error("expected an incorrect password to fail the check")
	}
}

func TestIsDuplicateKeyErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"duplicate entry", errors.New("Error 1062: Duplicate entry 'x' for key 'domain'"), true},
		{"unrelated error", errors.New("connection refused"), false},
	}

	for _, c := range cases {
		if got := isDuplicateKeyErr(c.err); got != c.want {
			t.Errorf("%s: isDuplicateKeyErr() = %v, want %v", c.name, got, c.want)
		}
	}
}

// Issue #99: the handler-level half of the login throttle (ratelimit_test.go
// covers the limiter itself). Two things matter here: the caller actually
// gets a 429 with a Retry-After once they're over the threshold, and a
// locked-out caller stops reaching the DB/bcrypt at all - otherwise the
// throttle would still let an attacker keep the bridge busy on their
// behalf.
func TestLoginRateLimitsRepeatedFailuresAndStopsDoingWork(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Exactly cLoginMaxFailures lookups are permitted; the attempts after
	// the lockout must not add any more.
	for i := 0; i < cLoginMaxFailures; i++ {
		mock.ExpectQuery("select `password_hash` from `admin_users`").
			WithArgs("operator").
			WillReturnRows(sqlmock.NewRows([]string{"password_hash"}).AddRow(mustHash(t, "the-real-password")))
	}

	a := Init(dao.NewWithDB(db), []byte("session-secret"))
	attempt := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/login",
			strings.NewReader(`{"username":"operator","password":"wrong-guess"}`))
		req.RemoteAddr = "203.0.113.7:54321"
		w := httptest.NewRecorder()
		a.Login(w, req)
		return w
	}

	for i := 0; i < cLoginMaxFailures; i++ {
		if got := attempt().Code; got != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, got, http.StatusUnauthorized)
		}
	}

	w := attempt()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status after the threshold = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header on the 429")
	}
	// A second rejected attempt, to be sure the lockout is sticky rather
	// than a one-shot.
	if got := attempt().Code; got != http.StatusTooManyRequests {
		t.Errorf("second attempt while locked out = %d, want %d", got, http.StatusTooManyRequests)
	}
	// Nothing beyond the permitted lookups reached the DB.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity while locked out: %v", err)
	}
}

// A lockout must be scoped to the address that earned it, so a guesser
// can't lock the real operator out of their own panel.
func TestLoginLockoutDoesNotAffectOtherAddresses(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	hash := mustHash(t, "the-real-password")
	for i := 0; i < cLoginMaxFailures+1; i++ {
		mock.ExpectQuery("select `password_hash` from `admin_users`").
			WillReturnRows(sqlmock.NewRows([]string{"password_hash"}).AddRow(hash))
	}

	a := Init(dao.NewWithDB(db), []byte("session-secret"))
	login := func(remoteAddr, password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/login",
			strings.NewReader(`{"username":"operator","password":"`+password+`"}`))
		req.RemoteAddr = remoteAddr
		w := httptest.NewRecorder()
		a.Login(w, req)
		return w
	}

	for i := 0; i < cLoginMaxFailures; i++ {
		login("203.0.113.7:1111", "wrong-guess")
	}
	if got := login("203.0.113.7:2222", "wrong-guess").Code; got != http.StatusTooManyRequests {
		t.Errorf("same host, different port = %d, want %d (should share one bucket)", got, http.StatusTooManyRequests)
	}
	if got := login("198.51.100.4:1111", "the-real-password").Code; got != http.StatusOK {
		t.Errorf("unrelated address with the correct password = %d, want %d", got, http.StatusOK)
	}
}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	return hash
}

// Issue #100: AddDevice used to mint the relay secret itself and hand it
// back in the 201 body, putting a long-lived credential somewhere it ends
// up in far more places than the one operator who needed it. The caller
// supplies it now, so there is nothing secret left to echo.
func TestAddDeviceNeverReturnsTheSecret(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef"
	mock.ExpectExec("insert into `devices`").
		WithArgs("owner-1", "someone.off-the.cloud", secret).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a := Init(dao.NewWithDB(db), []byte("session-secret"))
	body := `{"domain":"someone.off-the.cloud","ownerUuid":"owner-1","secret":"` + secret + `"}`
	w := httptest.NewRecorder()
	a.AddDevice(w, httptest.NewRequest(http.MethodPost, "/admin/api/devices", strings.NewReader(body)))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Errorf("the 201 body still echoes the relay secret: %s", w.Body.String())
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if _, present := got["secret"]; present {
		t.Error("expected no `secret` field in the response at all")
	}
	if got["domain"] != "someone.off-the.cloud" || got["ownerUuid"] != "owner-1" {
		t.Errorf("unexpected response body: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// With nothing generated server-side, an omitted secret can't silently
// become a registration nobody holds the credential for.
func TestAddDeviceRejectsAMissingOrWeakSecret(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"omitted", `{"domain":"someone.off-the.cloud"}`},
		{"empty", `{"domain":"someone.off-the.cloud","secret":""}`},
		{"whitespace only", `{"domain":"someone.off-the.cloud","secret":"        "}`},
		{"too short to be a relay credential", `{"domain":"someone.off-the.cloud","secret":"hunter2"}`},
	}

	for _, c := range cases {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		// No ExpectExec at all: nothing may reach the devices table.
		a := Init(dao.NewWithDB(db), []byte("session-secret"))
		w := httptest.NewRecorder()
		a.AddDevice(w, httptest.NewRequest(http.MethodPost, "/admin/api/devices", strings.NewReader(c.body)))

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", c.name, w.Code, http.StatusBadRequest)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		db.Close()
	}
}

// ownerUuid stays generated when omitted - it identifies the device, it
// isn't a credential, so returning it is the whole point.
func TestAddDeviceGeneratesOwnerUuidWhenOmitted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef"
	mock.ExpectExec("insert into `devices`").
		WithArgs(sqlmock.AnyArg(), "someone.off-the.cloud", secret).
		WillReturnResult(sqlmock.NewResult(1, 1))

	a := Init(dao.NewWithDB(db), []byte("session-secret"))
	body := `{"domain":"someone.off-the.cloud","secret":"` + secret + `"}`
	w := httptest.NewRecorder()
	a.AddDevice(w, httptest.NewRequest(http.MethodPost, "/admin/api/devices", strings.NewReader(body)))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got["ownerUuid"] == "" {
		t.Error("expected a generated ownerUuid in the response")
	}
}
