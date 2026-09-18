// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
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
