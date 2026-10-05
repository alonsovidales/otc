// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// newTestBridge serves mg's /ws on a test server and returns a dialer for
// it; the pool key (the Host clients connect with) is the server's address.
func newTestBridge(t *testing.T, mg *Manager) (dial func() *gorilla.Conn, host string) {
	t.Helper()
	if mg.upgrader.CheckOrigin == nil {
		mg.upgrader = gorilla.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	}
	srv := httptest.NewServer(http.HandlerFunc(mg.Listen))
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return func() *gorilla.Conn {
		t.Helper()
		c, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dialing bridge: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}, strings.TrimPrefix(srv.URL, "http://")
}

func registerFrame(t *testing.T, domain, owner, secret string) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqBridgeRegister{
		ReqBridgeRegister: &pb.BridgeRegister{Domain: domain, OwnerUuid: owner, Secret: secret},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readResp(t *testing.T, c *gorilla.Conn) *pb.RespEnvelope {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}
	return &resp
}

// A database failure while checking a registration is the bridge's own
// trouble, not a wrong secret: the device must not be told "Invalid
// Secret" (nor the security log get an invalid_secret event for it).
func TestRegistrationDBErrorIsNotAnInvalidSecret(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `owner_uuid`, `secret` from `devices` where `domain` = \\?").
		WillReturnError(errors.New("connection refused"))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, _ := newTestBridge(t, mg)
	c := dial()
	if err := c.WriteMessage(gorilla.BinaryMessage, registerFrame(t, "pit.otc", "owner-uuid", "secret")); err != nil {
		t.Fatal(err)
	}
	resp := readResp(t, c)
	if !resp.Error || resp.ErrorMessage != cInternalErrorMsg {
		t.Fatalf("got Error=%v %q, want the internal-error message", resp.Error, resp.ErrorMessage)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
