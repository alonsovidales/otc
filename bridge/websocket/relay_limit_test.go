// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// A device's registration is read with the small unpaired limit, and the
// same connection then becomes a relay: its replies (a whole photo for a
// preview) can be far bigger. The relay must not keep the registration's
// limit - it did, and every reply over 8 MB killed the relay ("device
// connection closed"), which the browser showed as the device being away.
func TestRelayTakesRepliesOverTheRegistrationLimit(t *testing.T) {
	big := strings.Repeat("x", cUnpairedReadLimit+1<<20) // 9 MB
	upgrader := gorilla.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req pb.ReqEnvelope
			if proto.Unmarshal(frame, &req) != nil {
				return
			}
			resp, _ := proto.Marshal(&pb.RespEnvelope{Id: req.Id, ErrorMessage: big})
			if conn.WriteMessage(gorilla.BinaryMessage, resp) != nil {
				return
			}
		}
	}))
	defer srv.Close()

	conn, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadLimit(cUnpairedReadLimit) // what reading the registration leaves behind
	relay := newDeviceRelay(conn, nil)
	defer relay.Close()

	resp, err := relay.forward(envelopeFrame(t, 7))
	if err != nil {
		t.Fatalf("a 9 MB reply killed the relay: %v", err)
	}
	if len(resp) < len(big) {
		t.Fatalf("short reply: %d bytes", len(resp))
	}
}
