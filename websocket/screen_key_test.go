// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/supervisor"
)

// Only the device's own browser is the screen: from loopback, no proxy in
// between, the device's page at a loopback address.
func TestLocalScreenIsTheDevicesOwnBrowser(t *testing.T) {
	for _, c := range []struct {
		name, peer, host, origin string
		header                   string
		want                     bool
	}{
		{"the kiosk", "127.0.0.1:50000", "127.0.0.1:8080", "http://127.0.0.1:8080", "", true},
		{"localhost", "127.0.0.1:50000", "localhost:8080", "http://localhost:8080", "", true},
		{"IPv6 loopback", "[::1]:50000", "[::1]:8080", "http://[::1]:8080", "", true},
		{"another computer", "192.168.1.20:50000", "127.0.0.1:8080", "http://127.0.0.1:8080", "", false},
		{"through Tailscale Funnel", "127.0.0.1:50000", "127.0.0.1:8080", "http://127.0.0.1:8080", "X-Forwarded-For", false},
		{"another page", "127.0.0.1:50000", "127.0.0.1:8080", "https://device.example.ts.net", "", false},
		{"a page at the device's name", "127.0.0.1:50000", "otc.local:8080", "http://otc.local:8080", "", false},
		{"Origin and Host apart", "127.0.0.1:50000", "otc.local:8080", "http://127.0.0.1:8080", "", false},
		{"an app (no Origin)", "127.0.0.1:50000", "127.0.0.1:8080", "", "", false},
		{"https", "127.0.0.1:50000", "127.0.0.1:8080", "https://127.0.0.1:8080", "", false},
	} {
		r := httptest.NewRequest("GET", "http://"+c.host+"/ws", nil)
		r.RemoteAddr, r.Host = c.peer, c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.header != "" {
			r.Header.Set(c.header, "203.0.113.9")
		}
		if got := localScreen(r); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	t.Setenv(supervisor.ChildEnvVar, "1")
	r := httptest.NewRequest("GET", "http://127.0.0.1:8081/ws", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Origin", "http://127.0.0.1:8081")
	if localScreen(r) {
		t.Error("a user's instance took it for the screen")
	}
}

// The screen signs in once and gets a key; a new connection (the device
// restarted, the page reloaded) signs in with it, again and again; any
// other connection is refused one and can't use it; the screen's sign-out
// ends it.
func TestScreenKeyKeepsTheScreenSignedIn(t *testing.T) {
	dir := t.TempDir()
	saved := session.ScreenKeyDir
	session.ScreenKeyDir = dir
	t.Cleanup(func() { session.ScreenKeyDir = saved })

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	d := dao.NewWithDB(db)
	secret := new([]byte)
	mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	mock.ExpectExec("insert into `vault`").WithArgs(keep{secret}, keep{new([]byte)}).WillReturnResult(sqlmock.NewResult(1, 1))
	ses, err := session.New("owner-uuid", "right-password", true, d)
	if err != nil {
		t.Fatal(err)
	}
	vault := func() {
		mock.ExpectQuery("select `secret` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"secret"}).AddRow(*secret))
	}
	mg := &Manager{dao: d}
	mg.backfillOnce.Do(func() {}) // nothing of the library to work through here

	issue := func(ch *connHandler) *pb.RespEnvelope {
		resp, _ := ch.processAuthRequest(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqIssueSessionToken{
			ReqIssueSessionToken: &pb.ReqIssueSessionToken{Screen: true}}}, "")
		return resp
	}
	redeem := func(ch *connHandler, key string) bool {
		resp, _ := ch.processNonAuthRequest(&pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqAuthWithToken{
			ReqAuthWithToken: &pb.ReqAuthWithToken{Token: key, Screen: true}}}, "")
		return resp.GetRespAck().GetOk() && ch.getSession() != nil
	}

	screen, _ := tracked(mg)
	screen.screen = true
	screen.setSession(ses)
	vault()
	resp := issue(screen)
	tok := resp.GetRespSessionToken().GetSessionToken()
	if resp.Error || tok.GetToken() == "" || tok.GetExpiresAtUnixMs() != 0 {
		t.Fatalf("the screen's key: %v", resp)
	}

	laptop, _ := tracked(mg)
	laptop.setSession(ses)
	if resp := issue(laptop); !resp.Error || resp.GetRespSessionToken() != nil {
		t.Errorf("another connection got a screen key: %v", resp)
	}
	if redeem(laptop, tok.Token) {
		t.Error("another connection signed in with the screen's key")
	}

	for i := 0; i < 2; i++ {
		again, _ := tracked(mg)
		again.screen = true
		vault()
		if !redeem(again, tok.Token) {
			t.Fatalf("the screen's sign-in %d with its key was refused", i+1)
		}
	}

	if resp, _ := screen.processAuthRequest(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqRevokeSessionToken{
		ReqRevokeSessionToken: &pb.ReqRevokeSessionToken{}}}, ""); !resp.GetRespAck().GetOk() {
		t.Fatalf("sign-out: %v", resp)
	}
	after, _ := tracked(mg)
	after.screen = true
	if redeem(after, tok.Token) {
		t.Error("the key still signs in after the screen signed out")
	}
}
