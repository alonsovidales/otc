// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/accounts"
	"github.com/alonsovidales/otc/bridge/dao"
	"golang.org/x/crypto/bcrypt"
)

// freshSessionCookie is sessionCookie for a sign-in made just now: a
// session issued at its full 30 days, which the recent-sign-in checks
// trust.
func freshSessionCookie(sessionSecret []byte, accountID string) *http.Cookie {
	key := hmac.New(sha256.New, sessionSecret)
	key.Write([]byte("account-session"))
	payload := fmt.Sprintf("%s|0|%d", accountID, time.Now().Add(30*24*time.Hour).Unix())
	mac := hmac.New(sha256.New, key.Sum(nil))
	mac.Write([]byte(payload))
	return &http.Cookie{Name: "__Host-otc_account", Value: payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}
}

func epochRow(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("select `session_epoch` from `accounts`").WillReturnRows(sqlmock.NewRows([]string{"session_epoch"}).AddRow(0))
}

// accountWithPassword is the account lookup for acc1, verified, with the
// terms accepted and hash as its password ("" for none).
func accountWithPassword(mock sqlmock.Sqlmock, hash string) {
	var h any
	if hash != "" {
		h = hash
	}
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at", "lang"}).
		AddRow("acc1", "a@b.c", "A", "B", "ES", h, time.Now(), time.Now(), time.Now(), true, accounts.TermsVersion, time.Now(), ""))
}

// Moving one of the account's names to a new device (a claim for a name it
// already has) relays every client of the name to that device: it needs a
// setup code made right after its owner signed in, or a session that is a
// sign-in just made. A code made for an old session, or an old session
// itself, still registers new names, but moves none - and such a refusal
// leaves the address's claim cooldown alone, so the claim after signing in
// again goes through.
func TestMovingANameNeedsAFreshSignIn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := []byte("test session secret")
	d := dao.NewWithDB(db)
	api := &API{muxHTTPServer: http.NewServeMux(), dao: d, accounts: accounts.Init(d, secret, "off-the.cloud"), lastClaimByAddr: map[string]time.Time{}}
	const identity = `"owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"`
	claim := func(name string, setup func(r *http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(`{"name":"`+name+`",`+identity+`}`))
		req.RemoteAddr = "203.0.113.7:1111"
		setup(req)
		rec := httptest.NewRecorder()
		api.claimName(rec, req)
		return rec
	}
	withCode := func(r *http.Request) { r.Header.Set("Authorization", "Bearer ABCD-2345") }
	code := func(confirmed bool) {
		mock.ExpectQuery("select `account_id`, `owner_confirmed` from `account_setup_codes`").
			WillReturnRows(sqlmock.NewRows([]string{"account_id", "owner_confirmed"}).AddRow("acc1", confirmed))
		accountRow(mock, true, accounts.TermsVersion)
	}
	ownName := func() {
		mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").WithArgs("casa.off-the.cloud").
			WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow("acc1"))
	}
	replaced := func() {
		mock.ExpectExec("update `devices` set `owner_uuid` = \\?, `secret` = \\?, `secret_hash` = \\? where `domain` = \\? and `account_id` = \\?").
			WithArgs("11111111-2222-3333-4444-555555555555", "0123456789abcdef0123456789abcdef01234567", dao.SecretHash("0123456789abcdef0123456789abcdef01234567"), "casa.off-the.cloud", "acc1").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	want := `{"code":"move_needs_signin","error":"that name is yours and in use on another device: to move it to this one, sign in again, or use a setup code made within 15 minutes of signing in to your account page"}` + "\n"

	// A code made for an old session: refused, nothing changed.
	code(false)
	ownName()
	if rec := claim("casa", withCode); rec.Code != http.StatusForbidden || rec.Body.String() != want {
		t.Errorf("an unconfirmed code moving a name: %d %s", rec.Code, rec.Body.String())
	}
	// ... but it registers a new name.
	code(false)
	mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").WithArgs("newpi.off-the.cloud").WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectQuery("select count\\(\\*\\) from `released_domains`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery("select count\\(\\*\\) from `devices` where `account_id` = \\?").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectExec("insert into `devices`").WillReturnResult(sqlmock.NewResult(1, 1))
	if rec := claim("newpi", withCode); rec.Code != http.StatusCreated {
		t.Errorf("an unconfirmed code registering a new name: %d %s", rec.Code, rec.Body.String())
	}
	api.lastClaimByAddr = map[string]time.Time{} // that claim took the cooldown
	// A code made right after a sign-in moves it.
	code(true)
	ownName()
	replaced()
	if rec := claim("casa", withCode); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"replaced":true`) {
		t.Errorf("a confirmed code moving a name: %d %s", rec.Code, rec.Body.String())
	}
	api.lastClaimByAddr = map[string]time.Time{}

	// The account page's own session: an old one doesn't move it, a sign-in
	// just made does.
	epochRow(mock)
	accountRow(mock, true, accounts.TermsVersion)
	epochRow(mock) // FreshSignIn
	ownName()
	if rec := claim("casa", func(r *http.Request) { r.AddCookie(sessionCookie(secret, "acc1")) }); rec.Code != http.StatusForbidden || rec.Body.String() != want {
		t.Errorf("an old session moving a name: %d %s", rec.Code, rec.Body.String())
	}
	epochRow(mock)
	accountRow(mock, true, accounts.TermsVersion)
	epochRow(mock)
	ownName()
	replaced()
	if rec := claim("casa", func(r *http.Request) { r.AddCookie(freshSessionCookie(secret, "acc1")) }); rec.Code != http.StatusCreated {
		t.Errorf("a sign-in just made moving a name: %d %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

// A new identity for one of the account's domains (the account page's New
// identity) needs the account's password, or for one that signs in only
// with Google or Apple a sign-in within 15 minutes: a session cookie alone
// is not enough to hand the name's clients to another device. Nothing is
// changed until it checks out.
func TestNewIdentityNeedsTheOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := []byte("test session secret")
	d := dao.NewWithDB(db)
	api := &API{muxHTTPServer: http.NewServeMux(), dao: d, accounts: accounts.Init(d, secret, "off-the.cloud")}
	reissue := func(body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/account/domains/casa.off-the.cloud/identity", strings.NewReader(body))
		req.SetPathValue("domain", "casa.off-the.cloud")
		req.RemoteAddr = "203.0.113.8:1111"
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		api.accountNewIdentity(rec, req, "acc1")
		return rec
	}
	reissued := func() {
		mock.ExpectExec("update `devices` set `owner_uuid` = \\?, `secret` = \\?, `secret_hash` = \\? where `domain` = \\? and `account_id` = \\?").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("the-owners-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{"", `{}`, `{"password":"a-guess"}`} {
		accountWithPassword(mock, string(hash))
		if rec := reissue(body, freshSessionCookie(secret, "acc1")); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"wrong_password"`) {
			t.Errorf("password account, body %q: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	accountWithPassword(mock, string(hash))
	reissued()
	rec := reissue(`{"password":"the-owners-password"}`, sessionCookie(secret, "acc1"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"secret"`) {
		t.Errorf("password account, its password: %d %s", rec.Code, rec.Body.String())
	}

	// Google or Apple only: the sign-in's age is what counts.
	accountWithPassword(mock, "")
	epochRow(mock)
	if rec := reissue(`{}`, sessionCookie(secret, "acc1")); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"recent_signin_required"`) {
		t.Errorf("provider account, an old session: %d %s", rec.Code, rec.Body.String())
	}
	accountWithPassword(mock, "")
	epochRow(mock)
	reissued()
	if rec := reissue(`{}`, freshSessionCookie(secret, "acc1")); rec.Code != http.StatusOK {
		t.Errorf("provider account, a sign-in just made: %d %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}
