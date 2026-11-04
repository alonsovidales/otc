// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/limits"
)

// Anyone can send any Host: an address that can't be a device's is never
// looked up (here every lookup would say "disabled"), and every device
// address is looked up only within the client's one-off requests.
func TestMadeUpHostsAreNeverLookedUp(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 10; i++ {
		mock.ExpectQuery("select `disabled`").WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(true))
	}
	api := siteAPI(t)
	api.dao = dao.NewWithDB(db)
	api.websocket = nil
	api.forwardOneOff = func(string, []byte) ([]byte, error) { return nil, errors.New("no connection") }
	get := func(host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		api.serveStatic(rec, req)
		return rec
	}
	for _, host := range []string{"198.51.100.20", "evil.example", "a.b.bridge.test", "pit.bridge.test:443"} {
		if rec := get(host); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "disabled") {
			t.Errorf("%s: %d, looked up", host, rec.Code)
		}
	}
	if rec := get("pit.bridge.test"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(strings.ToLower(rec.Body.String()), "disabled") {
		t.Errorf("a device's address: %d, not looked up", rec.Code)
	}

	// Past the address's one-off requests: 429, and no lookup - the one
	// query still expected stays unmet.
	db2, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	for i := 0; i < 2; i++ {
		mock2.ExpectQuery("select `disabled`").WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))
	}
	api.dao = dao.NewWithDB(db2)
	api.oneOffPerAddr = limits.NewRate(0.001, 1)
	get("pit.bridge.test")
	if rec := get("pit.bridge.test"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("past the limit: %d", rec.Code)
	}
	if mock2.ExpectationsWereMet() == nil {
		t.Error("a request past the limit was looked up")
	}
}

// device-online may ask Redis: one address gets an answer a second, after
// a burst - the setup wizard asks every 5 s.
func TestDeviceOnlineIsLimitedPerAddress(t *testing.T) {
	api := &API{onlinePerAddr: limits.NewRate(0.001, 2)}
	ask := func(addr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/device-online?name=casa", nil)
		req.RemoteAddr = addr + ":5555"
		rec := httptest.NewRecorder()
		api.deviceOnline(rec, req)
		return rec
	}
	for i := 0; i < 2; i++ {
		if rec := ask("198.51.100.7"); rec.Code != http.StatusOK {
			t.Fatalf("answer %d: %d", i+1, rec.Code)
		}
	}
	rec := ask("198.51.100.7")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("past the limit: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := ask("203.0.113.9"); rec.Code != http.StatusOK {
		t.Errorf("another address: %d", rec.Code)
	}
}
