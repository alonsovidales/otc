// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/accounts"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/limits"
	"github.com/alonsovidales/otc/bridge/websocket"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
	"google.golang.org/protobuf/proto"
)

func TestHealthcheck(t *testing.T) {
	// registerAPIs also wires up api.websocket.Listen on a nil *websocket.Manager;
	// that's fine as long as nothing exercises the websocket endpoint here.
	api := &API{muxHTTPServer: http.NewServeMux()}
	api.registerAPIs()

	req := httptest.NewRequest(http.MethodGet, cHealtyPath, nil)
	rec := httptest.NewRecorder()
	api.muxHTTPServer.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "OK" {
		t.Errorf("expected body %q, got %q", "OK", rec.Body.String())
	}
}

// Issue #99: the contact cooldown used to bucket on r.RemoteAddr with its
// port still attached, so a sender opening a fresh connection per request
// - which is what a script does by default - drew a new ephemeral port,
// landed in a brand new bucket, and sailed straight past the cooldown
// while growing the map an entry per port. It buckets by host now.
//
// The cooldown sits after the honeypot and field validation but before the
// insert, so this needs a stand-in DB: the two submissions that get past
// the cooldown each store a request, the one that's throttled must not.
func TestContactCooldownIgnoresTheSourcePort(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectExec("insert into `contact_requests`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("insert into `contact_requests`").WillReturnResult(sqlmock.NewResult(2, 1))

	api := &API{
		muxHTTPServer:     http.NewServeMux(),
		dao:               dao.NewWithDB(db),
		lastContactByAddr: map[string]time.Time{},
	}

	submit := func(remoteAddr string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/contact",
			strings.NewReader(`{"name":"a","email":"a@b.c","message":"hi"}`))
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		api.submitContact(rec, req)
		return rec.Code
	}

	if got := submit("203.0.113.7:1111"); got != http.StatusCreated {
		t.Fatalf("first submission = %d, want %d", got, http.StatusCreated)
	}
	if got := submit("203.0.113.7:2222"); got != http.StatusTooManyRequests {
		t.Errorf("second submission from a new port = %d, want %d", got, http.StatusTooManyRequests)
	}
	if got := submit("198.51.100.4:1111"); got != http.StatusCreated {
		t.Errorf("submission from an unrelated host = %d, want %d", got, http.StatusCreated)
	}

	// One entry per host, not one per connection.
	if len(api.lastContactByAddr) != 2 {
		t.Errorf("tracked %d addresses, want 2 (one per host)", len(api.lastContactByAddr))
	}
	// The throttled submission must not have reached the DB.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

// Issue #97: a device that's switched off, offline, or still booting used
// to surface as a bare 502 with an empty body - see the screenshot on the
// issue. It gets the bridge's own "not available right now" page now, with
// a 503 (temporarily absent, try again) rather than a 502 (broken
// upstream), so both a person and anything machine-read gets told the same
// thing.
func TestUnreachableDeviceGetsTheUnavailablePageNotABareGateway(t *testing.T) {
	staticDir := t.TempDir() + "/"
	const body = "<h1>This device isn't available right now</h1>"
	if err := os.WriteFile(staticDir+"unavailable.html", []byte(body), 0644); err != nil {
		t.Fatalf("writing unavailable.html: %v", err)
	}

	// A Manager with no device registered for this domain is exactly what
	// an offline device looks like from here: ForwardOneOff finds no
	// connection to hand the request to.
	api := &API{
		muxHTTPServer: http.NewServeMux(),
		websocket:     websocket.Init("", nil),
		staticPath:    staticDir,
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "someone.off-the.cloud"
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	api.proxyStaticAsset(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("body = %q, want the unavailable page", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header so machine clients know to come back")
	}
}

// The same failure while the browser is fetching a script or stylesheet
// must not answer with an HTML page - a browser refuses that, noisily,
// on top of whatever actually went wrong.
func TestUnreachableDeviceSendsNoHTMLBodyForNonPageRequests(t *testing.T) {
	staticDir := t.TempDir() + "/"
	if err := os.WriteFile(staticDir+"unavailable.html", []byte("<h1>nope</h1>"), 0644); err != nil {
		t.Fatalf("writing unavailable.html: %v", err)
	}

	api := &API{
		muxHTTPServer: http.NewServeMux(),
		websocket:     websocket.Init("", nil),
		staticPath:    staticDir,
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
	req.Host = "someone.off-the.cloud"
	req.Header.Set("Accept", "*/*")
	rec := httptest.NewRecorder()
	api.proxyStaticAsset(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want it empty for a non-page request", rec.Body.String())
	}
}

// fakeStaticDevice answers ReqGetStaticAsset like a device: "x" with the
// content type types[path] gives (text/html for a missing file, which the
// device answers with index.html).
func fakeStaticDevice(types map[string]string) func(string, []byte) ([]byte, error) {
	return func(_ string, frame []byte) ([]byte, error) {
		var req pb.ReqEnvelope
		if err := proto.Unmarshal(frame, &req); err != nil {
			return nil, err
		}
		return proto.Marshal(&pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespStaticAsset{
			RespStaticAsset: &pb.RespStaticAsset{Content: []byte("x"), ContentType: types[req.GetReqGetStaticAsset().GetPath()]},
		}})
	}
}

// Vite's hashed build files are kept by the browser for good; nothing
// else is - least of all index.html answering for a missing asset.
func TestOnlyHashedAssetsAreCachedForGood(t *testing.T) {
	types := map[string]string{
		"/assets/index-CaKPC84J.js":     "text/javascript; charset=utf-8",
		"/assets/index-Bgzu-j_J.css":    "text/css; charset=utf-8",
		"/assets/index-ZZZZZZZZ.js":     "text/html; charset=utf-8",
		"/":                             "text/html; charset=utf-8",
		"/sw.js":                        "text/javascript; charset=utf-8",
		"/favicon-32x32.png":            "image/png",
		"/shared/assets/x-CaKPC84J.png": "image/png",
	}
	api := &API{forwardOneOff: fakeStaticDevice(types)}
	for path, ct := range types {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "pit.off-the.cloud"
		rec := httptest.NewRecorder()
		api.proxyStaticAsset(rec, req)
		cached := rec.Header().Get("Cache-Control") == "public, max-age=31536000, immutable"
		want := strings.HasPrefix(path, "/assets/") && !strings.HasPrefix(ct, "text/html")
		if rec.Code != http.StatusOK || rec.Body.String() != "x" || rec.Header().Get("Content-Type") != ct {
			t.Errorf("%s: %d %q %q", path, rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
		}
		if cached != want {
			t.Errorf("%s: cached for good %v, want %v", path, cached, want)
		}
	}
}

// A missing page file must still produce the right status rather than a
// 200 with nothing in it.
func TestServeOwnPageFallsBackToTheStatusWhenTheFileIsMissing(t *testing.T) {
	api := &API{muxHTTPServer: http.NewServeMux(), staticPath: t.TempDir() + "/"}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	api.serveOwnPage(rec, req, "does-not-exist.html", http.StatusServiceUnavailable)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// Issue #38: the setup wizard reserves a device's name before installing.
func TestClaimNameReservesAFreeNameAndRefusesATakenOne(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	// First claim: free, inserted (no accounts wired here, so the claim
	// is an open registration - issue #124's account path is exercised
	// against a real database, see the integration notes in CLAUDE.md).
	mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").
		WithArgs("newpi.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	// Not a recently released name (held for its previous account).
	mock.ExpectQuery("select count\\(\\*\\) from `released_domains`").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `devices`").
		WithArgs("11111111-2222-3333-4444-555555555555", "newpi.off-the.cloud", "0123456789abcdef0123456789abcdef01234567").
		WillReturnResult(sqlmock.NewResult(1, 1))
	// Second claim (another address): already there.
	mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").
		WithArgs("newpi.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(nil))

	api := &API{
		muxHTTPServer:   http.NewServeMux(),
		dao:             dao.NewWithDB(db),
		lastClaimByAddr: map[string]time.Time{},
	}
	claim := func(remoteAddr, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(body))
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		api.claimName(rec, req)
		return rec
	}
	good := `{"name":"NewPi","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`

	if rec := claim("203.0.113.7:1111", good); rec.Code != http.StatusCreated {
		t.Fatalf("free name: %d %s, want 201", rec.Code, rec.Body.String())
	}
	if rec := claim("203.0.113.8:1111", good); rec.Code != http.StatusConflict {
		t.Errorf("taken name: %d, want 409", rec.Code)
	}
	if rec := claim("203.0.113.7:2222", good); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second claim from the same host within the cooldown: %d, want 429", rec.Code)
	}
	if rec := claim("203.0.113.9:1111", `{"name":"www","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("reserved name: %d, want 400", rec.Code)
	}
	if rec := claim("203.0.113.10:1111", `{"name":"ok-name","owner_uuid":"x","secret":"short"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("blank identity: %d, want 400", rec.Code)
	}
	if rec := claim("203.0.113.11:1111", `{"name":"Bad Name!","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid name: %d, want 400", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

func TestNameAvailableAnswersForFreeTakenAndReservedNames(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select 1 from `devices` where `domain` = \\?").
		WithArgs("free.off-the.cloud").WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery("select 1 from `devices` where `domain` = \\?").
		WithArgs("pit.off-the.cloud").WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	api := &API{muxHTTPServer: http.NewServeMux(), dao: dao.NewWithDB(db)}
	ask := func(name string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/name-available?name="+name, nil)
		rec := httptest.NewRecorder()
		api.nameAvailable(rec, req)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	if code, body := ask("free"); code != 200 || !strings.Contains(body, `"available":true`) {
		t.Errorf("free: %d %s", code, body)
	}
	if code, body := ask("pit"); code != 200 || !strings.Contains(body, `"available":false`) {
		t.Errorf("taken: %d %s", code, body)
	}
	if code, body := ask("admin"); code != 200 || !strings.Contains(body, `"available":false`) {
		t.Errorf("reserved: %d %s", code, body)
	}
	if code, _ := ask("no_underscores"); code != http.StatusBadRequest {
		t.Errorf("invalid: %d, want 400", code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

// Issue #38: the LAN-address hand-off between a device that just joined
// the owner's WiFi and the wizard page still open on their phone.
func TestSetupBeaconHandOff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	token := "abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	other := "abcdefghijklmnopqrstuvwxyz0123456789ABCX"
	// lookup before: nothing
	mock.ExpectQuery("select `addr` from `setup_beacons`").WithArgs(token, 10).
		WillReturnRows(sqlmock.NewRows([]string{"addr"}))
	// the valid report (the two invalid ones never reach the DB)
	mock.ExpectExec("delete from `setup_beacons`").WithArgs(10).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("insert into `setup_beacons`").WithArgs(token, "192.168.1.20").WillReturnResult(sqlmock.NewResult(0, 1))
	// lookup after: the address; another token: nothing
	mock.ExpectQuery("select `addr` from `setup_beacons`").WithArgs(token, 10).
		WillReturnRows(sqlmock.NewRows([]string{"addr"}).AddRow("192.168.1.20"))
	mock.ExpectQuery("select `addr` from `setup_beacons`").WithArgs(other, 10).
		WillReturnRows(sqlmock.NewRows([]string{"addr"}))

	api := &API{muxHTTPServer: http.NewServeMux(), dao: dao.NewWithDB(db)}

	lookup := func(tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/setup-lookup?token="+tok, nil)
		rec := httptest.NewRecorder()
		api.setupLookup(rec, req)
		return rec
	}
	beacon := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/setup-beacon", strings.NewReader(body))
		rec := httptest.NewRecorder()
		api.setupBeacon(rec, req)
		return rec.Code
	}

	if rec := lookup(token); rec.Code != http.StatusNotFound {
		t.Fatalf("before any report: %d, want 404", rec.Code)
	}
	if code := beacon(`{"token":"` + token + `","addr":"8.8.8.8"}`); code != http.StatusBadRequest {
		t.Errorf("public address accepted: %d, want 400", code)
	}
	if code := beacon(`{"token":"short","addr":"192.168.1.20"}`); code != http.StatusBadRequest {
		t.Errorf("short token accepted: %d, want 400", code)
	}
	if code := beacon(`{"token":"` + token + `","addr":"192.168.1.20"}`); code != http.StatusNoContent {
		t.Fatalf("report: %d, want 204", code)
	}
	rec := lookup(token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"addr":"192.168.1.20"`) {
		t.Errorf("after the report: %d %s, want the address", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("the page polls from another origin - CORS must be open")
	}
	if rec := lookup(other); rec.Code != http.StatusNotFound {
		t.Errorf("another token: %d, want 404", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

// A released name is held for its previous account for 30 days: another
// account (or an anonymous claim) can't take it.
func TestClaimRefusesARecentlyReleasedName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").
		WithArgs("gone.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectQuery("select count\\(\\*\\) from `released_domains`").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))

	api := &API{muxHTTPServer: http.NewServeMux(), dao: dao.NewWithDB(db), lastClaimByAddr: map[string]time.Time{}}
	req := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(
		`{"name":"gone","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`))
	req.RemoteAddr = "203.0.113.9:1111"
	rec := httptest.NewRecorder()
	api.claimName(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "30 days") {
		t.Errorf("a held name was claimable: %d %s", rec.Code, rec.Body.String())
	}
}

// Device sites are same-site subdomains: their pages must not be able to
// act on the bridge's account or admin endpoints with a visitor's cookie.
func TestOriginGuard(t *testing.T) {
	api := &API{tld: "off-the.cloud"}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := api.originGuard(ok)
	try := func(method, path, origin string, cookie bool) int {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie {
			req.AddCookie(&http.Cookie{Name: "__Host-otc_account", Value: "x"})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	cases := []struct {
		method, path, origin string
		cookie               bool
		want                 int
	}{
		{"POST", "/api/claim", "https://mallory.off-the.cloud", true, http.StatusForbidden},
		{"POST", "/api/account/login", "https://mallory.off-the.cloud", false, http.StatusForbidden},
		{"DELETE", "/admin/api/devices/x", "https://mallory.off-the.cloud", true, http.StatusForbidden},
		{"POST", "/api/account/domains", "", true, http.StatusForbidden},                      // a cookie without an Origin
		{"POST", "/api/account/domains", "https://off-the.cloud", true, http.StatusNoContent}, // the account page itself
		{"POST", "/api/claim", "", false, http.StatusNoContent},                               // the setup wizard (token, no cookie)
		{"GET", "/api/account/me", "https://mallory.off-the.cloud", true, http.StatusNoContent},
		{"POST", "/account/auth/apple/callback", "https://appleid.apple.com", false, http.StatusNoContent},
	}
	for _, c := range cases {
		if got := try(c.method, c.path, c.origin, c.cookie); got != c.want {
			t.Errorf("%s %s from %q (cookie %v): %d, want %d", c.method, c.path, c.origin, c.cookie, got, c.want)
		}
	}
}

// sessionCookie is the account page's session for accountID at epoch 0,
// signed the way package accounts signs it (sessionToken) under
// sessionSecret.
func sessionCookie(sessionSecret []byte, accountID string) *http.Cookie {
	key := hmac.New(sha256.New, sessionSecret)
	key.Write([]byte("account-session"))
	payload := fmt.Sprintf("%s|0|%d", accountID, time.Now().Add(time.Hour).Unix())
	mac := hmac.New(sha256.New, key.Sum(nil))
	mac.Write([]byte(payload))
	return &http.Cookie{Name: "__Host-otc_account", Value: payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}
}

func accountRow(mock sqlmock.Sqlmock, verified bool, terms string) {
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(
		[]string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at"}).
		AddRow("acc1", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), verified, terms, time.Now()))
}

// A claim with the account page's session follows the account page's
// rules: no name for an email nobody proved, or before the terms in force
// are accepted. A verified account's claim goes through.
func TestClaimWithASessionNeedsAVerifiedAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := []byte("test session secret")
	d := dao.NewWithDB(db)
	api := &API{muxHTTPServer: http.NewServeMux(), dao: d, accounts: accounts.Init(d, secret, "off-the.cloud"), lastClaimByAddr: map[string]time.Time{}}
	claim := func(remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(
			`{"name":"newpi","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`))
		req.RemoteAddr = remoteAddr
		req.AddCookie(sessionCookie(secret, "acc1"))
		rec := httptest.NewRecorder()
		api.claimName(rec, req)
		return rec
	}
	epoch := func() {
		mock.ExpectQuery("select `session_epoch` from `accounts`").WillReturnRows(sqlmock.NewRows([]string{"session_epoch"}).AddRow(0))
	}

	epoch()
	accountRow(mock, false, accounts.TermsVersion)
	if rec := claim("203.0.113.7:1111"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "confirm your email") {
		t.Errorf("unverified: %d %s, want 403", rec.Code, rec.Body.String())
	}
	epoch()
	accountRow(mock, true, accounts.TermsVersion)
	accountRow(mock, true, "2020-01-01")
	if rec := claim("203.0.113.7:1111"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "terms of use") {
		t.Errorf("old terms: %d %s, want 403", rec.Code, rec.Body.String())
	}
	// Neither refusal used the address's claim cooldown.
	epoch()
	accountRow(mock, true, accounts.TermsVersion)
	accountRow(mock, true, accounts.TermsVersion)
	mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectQuery("select count\\(\\*\\) from `released_domains`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery("select count\\(\\*\\) from `devices` where `account_id` = \\?").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `devices`").WillReturnResult(sqlmock.NewResult(1, 1))
	if rec := claim("203.0.113.7:1111"); rec.Code != http.StatusCreated {
		t.Errorf("verified: %d %s, want 201", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

// A 500 that stored nothing says "try again": the retry must not be
// refused by the cooldown the failed request started.
func TestRetryAfterAFailedStoreIsNotThrottled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("insert into `contact_requests`").WillReturnError(errors.New("driver: bad connection"))
	mock.ExpectExec("insert into `contact_requests`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").WillReturnError(errors.New("driver: bad connection"))
	mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectQuery("select count\\(\\*\\) from `released_domains`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `devices`").WillReturnResult(sqlmock.NewResult(1, 1))

	api := &API{muxHTTPServer: http.NewServeMux(), dao: dao.NewWithDB(db), lastContactByAddr: map[string]time.Time{}, lastClaimByAddr: map[string]time.Time{}}
	contact := func(remoteAddr string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/contact", strings.NewReader(`{"name":"a","email":"a@b.c","message":"hi"}`))
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		api.submitContact(rec, req)
		return rec.Code
	}
	claim := func(remoteAddr string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(
			`{"name":"newpi","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`))
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		api.claimName(rec, req)
		return rec.Code
	}

	if got := contact("203.0.113.7:1111"); got != http.StatusInternalServerError {
		t.Fatalf("failed store: %d, want 500", got)
	}
	if got := contact("203.0.113.7:2222"); got != http.StatusCreated {
		t.Errorf("retry: %d, want 201", got)
	}
	if got := contact("203.0.113.7:3333"); got != http.StatusTooManyRequests {
		t.Errorf("after a stored message: %d, want 429", got)
	}
	if got := claim("203.0.113.7:1111"); got != http.StatusInternalServerError {
		t.Fatalf("failed claim: %d, want 500", got)
	}
	if got := claim("203.0.113.7:2222"); got != http.StatusCreated {
		t.Errorf("claim retry: %d, want 201", got)
	}
	if got := claim("203.0.113.7:3333"); got != http.StatusTooManyRequests {
		t.Errorf("after a claim: %d, want 429", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

// Only a duplicate key means another claim took the name first; a
// database failure must not tell the person a free name is taken.
func TestClaimInsertFailureIsNotTaken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := &API{muxHTTPServer: http.NewServeMux(), dao: dao.NewWithDB(db), lastClaimByAddr: map[string]time.Time{}}
	for i, c := range []struct {
		err  error
		want int
	}{
		{errors.New("driver: bad connection"), http.StatusInternalServerError},
		{&mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'newpi.off-the.cloud' for key 'domain'"}, http.StatusConflict},
	} {
		mock.ExpectQuery("select `account_id` from `devices` where `domain` = \\?").WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
		mock.ExpectQuery("select count\\(\\*\\) from `released_domains`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		mock.ExpectExec("insert into `devices`").WillReturnError(c.err)
		req := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(
			`{"name":"newpi","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`))
		req.RemoteAddr = fmt.Sprintf("203.0.113.%d:1111", i+1)
		rec := httptest.NewRecorder()
		api.claimName(rec, req)
		if rec.Code != c.want {
			t.Errorf("insert error %v: %d %s, want %d", c.err, rec.Code, rec.Body.String(), c.want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}

// Setup-beacon reports are limited per address: one device reporting
// every few seconds never notices, a loop of made-up tokens does.
func TestSetupBeaconIsLimitedPerAddress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("delete from `setup_beacons`").WillReturnResult(sqlmock.NewResult(0, 0))
	for i := 0; i < cSetupBeaconBurst+1; i++ {
		mock.ExpectExec("insert into `setup_beacons`").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	api := &API{muxHTTPServer: http.NewServeMux(), dao: dao.NewWithDB(db), beaconPerAddr: limits.NewRate(cSetupBeaconPerSecond, cSetupBeaconBurst)}
	beacon := func(remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/setup-beacon", strings.NewReader(`{"token":"abcdefghijklmnopqrstuvwxyz0123456789ABCD","addr":"192.168.1.20"}`))
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		api.setupBeacon(rec, req)
		return rec
	}
	for i := 0; i < cSetupBeaconBurst; i++ {
		if rec := beacon("203.0.113.7:1111"); rec.Code != http.StatusNoContent {
			t.Fatalf("report %d: %d, want 204", i, rec.Code)
		}
	}
	if rec := beacon("203.0.113.7:2222"); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("past the burst: %d, want 429 with Retry-After", rec.Code)
	}
	if rec := beacon("198.51.100.4:1111"); rec.Code != http.StatusNoContent {
		t.Errorf("another address: %d, want 204", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}
