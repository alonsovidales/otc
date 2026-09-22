// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/websocket"
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
	// First claim: free, inserted.
	mock.ExpectQuery("select 1 from `devices` where `domain` = \\?").
		WithArgs("newpi.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectExec("insert into `devices`").
		WithArgs("11111111-2222-3333-4444-555555555555", "newpi.off-the.cloud", "0123456789abcdef0123456789abcdef01234567").
		WillReturnResult(sqlmock.NewResult(1, 1))
	// Second claim (another address): already there.
	mock.ExpectQuery("select 1 from `devices` where `domain` = \\?").
		WithArgs("newpi.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

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
