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
	"github.com/alonsovidales/otc/bridge/limits"
)

// The unauthenticated lookups - a name's availability, a claim (whose
// setup code is looked up first), a setup hand-off - are bounded per
// address (an IPv6 /64) and per network (an IPv4 /24, an IPv6 /48) before
// anything is asked: past either, a 429 with Retry-After and no query.
func TestLookupsAreBoundedBeforeTheDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := &API{muxHTTPServer: http.NewServeMux(), dao: dao.NewWithDB(db), lastClaimByAddr: map[string]time.Time{},
		lookupPerAddr: limits.NewRate(1.0/3600, 2), lookupPerNet: limits.NewRate(1.0/3600, 3)}
	type lookup struct {
		name                 string
		call                 func(http.ResponseWriter, *http.Request)
		method, target, body string
		code                 string
	}
	lookups := []lookup{
		{"name-available", api.nameAvailable, http.MethodGet, "/api/name-available?name=casa", "", "too_many_claims"},
		{"claim", api.claimName, http.MethodPost, "/api/claim", `{"name":"casa","owner_uuid":"11111111-2222-3333-4444-555555555555","secret":"0123456789abcdef0123456789abcdef01234567"}`, "too_many_claims"},
		{"setup-lookup", api.setupLookup, http.MethodGet, "/api/setup-lookup?token=abcdefghijklmnopqrstuvwxyz0123456789ABCD", "", "too_many_requests"},
	}
	ask := func(l lookup, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(l.method, l.target, strings.NewReader(l.body))
		req.Header.Set("Authorization", "Bearer ABCD-2345")
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		l.call(rec, req)
		return rec
	}
	available := lookups[0]
	free := func() {
		mock.ExpectQuery("select 1 from `devices` where `domain` = \\?").WithArgs("casa.off-the.cloud").WillReturnRows(sqlmock.NewRows([]string{"1"}))
	}
	allowed := func(what, remote string) {
		t.Helper()
		free()
		if rec := ask(available, remote); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", what, rec.Code, rec.Body.String())
		}
	}
	refused := func(what string, l lookup, remote string) {
		t.Helper()
		rec := ask(l, remote)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"code":"`+l.code+`"`) {
			t.Errorf("%s: %d %s, Retry-After %q", what, rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
		}
	}

	allowed("an address's first", "203.0.113.7:1000")
	allowed("an address's second", "203.0.113.7:1000")
	for _, l := range lookups {
		refused(l.name+", past the address's bound", l, "203.0.113.7:2000")
	}
	// Another address of the /24 gets the network's last one.
	allowed("another address of the /24", "203.0.113.8:1000")
	refused("the /24, spent", available, "203.0.113.8:1000")
	allowed("another network", "198.51.100.4:1000")

	// An IPv6 /64 is one address; its /48 is one network.
	allowed("a /64", "[2001:db8:1:1::1]:1000")
	allowed("another address of the /64", "[2001:db8:1:1::2]:1000")
	refused("a third address of the /64", available, "[2001:db8:1:1::3]:1000")
	allowed("another /64 of the /48", "[2001:db8:1:2::1]:1000")
	refused("the /48, spent", available, "[2001:db8:1:3::1]:1000")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB activity: %v", err)
	}
}
