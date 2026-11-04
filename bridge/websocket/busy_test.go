// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func busyPong(ids ...int32) []byte {
	b := []byte(cBusyPrefix)
	for _, id := range ids {
		b = protowire.AppendVarint(b, uint64(uint32(id)))
	}
	return b
}

// newWorkingDeviceServer stands in for a device that takes work(id) to
// answer each request and, while it works, names named(id) every 30 ms in
// a pong, as devices do (websocket's bridge_busy.go).
func newWorkingDeviceServer(t *testing.T, work func(id int32) time.Duration, named func(id int32) []int32) string {
	t.Helper()
	upgrader := gorilla.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var writeMu sync.Mutex
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return
			}
			id, _ := envelopeID(frame)
			go func(id int32) {
				done := time.After(work(id))
				tick := time.NewTicker(30 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-tick.C:
						if ids := named(id); len(ids) > 0 {
							conn.WriteControl(gorilla.PongMessage, busyPong(ids...), time.Now().Add(time.Second))
						}
						continue
					case <-done:
					}
					break
				}
				b, _ := proto.Marshal(&pb.RespEnvelope{Id: id, ErrorMessage: "done"})
				writeMu.Lock()
				defer writeMu.Unlock()
				conn.WriteMessage(gorilla.BinaryMessage, b)
			}(id)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func self(id int32) []int32 { return []int32{id} }

// A request the device keeps saying it works on is waited for past
// cForwardTimeout, which runs from the last time it said so.
func TestARequestTheDeviceWorksOnIsWaitedFor(t *testing.T) {
	setVar(t, &cForwardTimeout, 500*time.Millisecond)
	relay := dialRelay(t, newWorkingDeviceServer(t, func(int32) time.Duration { return 1500 * time.Millisecond }, self))
	defer relay.Close()
	start := time.Now()
	resp, release, err := relay.forward(envelopeFrame(t, 7))
	if err != nil {
		t.Fatalf("a request still being worked on was given up after %v: %v", time.Since(start), err)
	}
	release()
	if len(resp) == 0 {
		t.Fatal("no reply")
	}
}

// One the device doesn't name - it forgot it, or never got it - still
// times out after cForwardTimeout, whatever else it names; and one it
// names forever is given up at cForwardMaxWait.
func TestARequestNobodyWorksOnStillTimesOut(t *testing.T) {
	setVar(t, &cForwardTimeout, 200*time.Millisecond)
	relay := dialRelay(t, newWorkingDeviceServer(t, func(int32) time.Duration { return time.Hour }, func(int32) []int32 { return []int32{99} }))
	defer relay.Close()
	start := time.Now()
	if _, _, err := relay.forward(envelopeFrame(t, 7)); err == nil {
		t.Fatal("a request the device never named was answered")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("gave up after %v", d)
	}

	setVar(t, &cForwardTimeout, 500*time.Millisecond)
	setVar(t, &cForwardMaxWait, 1500*time.Millisecond)
	forever := dialRelay(t, newWorkingDeviceServer(t, func(int32) time.Duration { return time.Hour }, self))
	defer forever.Close()
	start = time.Now()
	if _, _, err := forever.forward(envelopeFrame(t, 8)); err == nil {
		t.Fatal("a request named forever was answered")
	}
	if d := time.Since(start); d < cForwardMaxWait || d > cForwardMaxWait+3*time.Second {
		t.Errorf("a request named forever was given up after %v, want about %v", d, cForwardMaxWait)
	}
}

func TestBusyIDs(t *testing.T) {
	if ids := busyIDs(string(busyPong(1, 300, -5))); len(ids) != 3 || ids[0] != 1 || ids[1] != 300 || ids[2] != -5 {
		t.Errorf("busyIDs = %v", ids)
	}
	for _, p := range []string{"", "otcbusy", "hello", "otcbusy\xff", "busy\x01"} {
		if ids := busyIDs(p); len(ids) != 0 {
			t.Errorf("busyIDs(%q) = %v", p, ids)
		}
	}
}

// Through the bridge: a client whose request takes the device longer than
// cForwardTimeout, while the device says it works on it, gets the device's
// answer and keeps its session; it used to be told the device was
// unreachable, and hung up on.
func TestALongRequestKeepsTheClientsSession(t *testing.T) {
	setVar(t, &cForwardTimeout, 500*time.Millisecond)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))
	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, host := newTestBridge(t, mg)
	t.Cleanup(func() { stopOfflineTimer(mg, host) })
	poolRelayTo(t, mg, host, newWorkingDeviceServer(t, func(id int32) time.Duration {
		if id == 2 {
			return 1500 * time.Millisecond
		}
		return 0
	}, self))

	c := dial()
	for id := int32(1); id <= 3; id++ {
		if err := c.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, id)); err != nil {
			t.Fatal(err)
		}
		resp := readResp(t, c)
		if resp.Id != id || resp.ErrorMessage != "done" {
			t.Fatalf("request %d: %v", id, resp)
		}
	}
}
