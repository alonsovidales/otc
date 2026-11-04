// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// busyIDsOf reads the ids of a pong payload naming requests still running.
func busyIDsOf(payload string) []int32 {
	rest, ok := strings.CutPrefix(payload, BusyPrefix)
	if !ok {
		return nil
	}
	var ids []int32
	for b := []byte(rest); len(b) > 0; {
		v, n := protowire.ConsumeVarint(b)
		if n < 0 {
			return nil
		}
		ids = append(ids, int32(uint32(v)))
		b = b[n:]
	}
	return ids
}

// busyHarness serves one connection with serveConnection, as from the
// bridge (fromBridge) or not, its requests held until release is closed;
// the returned client plays the bridge, its pongs' busy ids collected.
func busyHarness(t *testing.T, fromBridge bool, release chan struct{}) (c *gorilla.Conn, named func() map[int32]int) {
	t.Helper()
	saved := handleRequest
	handleRequest = func(ch *connHandler, env *pb.ReqEnvelope) (*pb.RespEnvelope, bool) {
		<-release
		return &pb.RespEnvelope{Id: env.Id}, false
	}
	served := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("the connection was never done")
		}
		handleRequest = saved
	})
	mg := &Manager{}
	up := gorilla.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer close(served)
		defer conn.Close()
		if fromBridge {
			r = nil // how openBridgeConn serves the connections it dials
		}
		mg.serveConnection(conn, r, nil)
	}))
	t.Cleanup(srv.Close)
	c, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	var mu sync.Mutex
	seen := map[int32]int{}
	c.SetPongHandler(func(data string) error {
		mu.Lock()
		defer mu.Unlock()
		for _, id := range busyIDsOf(data) {
			seen[id]++
		}
		return nil
	})
	return c, func() map[int32]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[int32]int{}
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
}

func sendEnvelope(t *testing.T, c *gorilla.Conn, id int32) {
	t.Helper()
	b, _ := proto.Marshal(&pb.ReqEnvelope{Id: id})
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		t.Fatal(err)
	}
}

// On a connection to the bridge, a request still running is named in a
// pong every cBusyEvery once it has run cBusyAfter, until it is answered.
func TestARequestStillRunningIsNamedToTheBridge(t *testing.T) {
	setBusy(t, 50*time.Millisecond, 20*time.Millisecond)
	release := make(chan struct{})
	c, named := busyHarness(t, true, release)
	answers := make(chan int32, 4)
	go func() {
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			var resp pb.RespEnvelope
			if proto.Unmarshal(data, &resp) == nil {
				answers <- resp.Id
			}
		}
	}()
	sendEnvelope(t, c, 7)
	sendEnvelope(t, c, 300) // a two-byte varint
	deadline := time.Now().Add(5 * time.Second)
	for named()[7] < 2 || named()[300] < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("named %v, want ids 7 and 300 named again and again", named())
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-answers:
		case <-time.After(5 * time.Second):
			t.Fatal("no answer")
		}
	}
	time.Sleep(50 * time.Millisecond)
	before := named()
	time.Sleep(200 * time.Millisecond)
	if after := named(); after[7] != before[7] || after[300] != before[300] {
		t.Errorf("answered requests still named: %v then %v", before, after)
	}
}

// A connection that isn't the bridge's never gets them.
func TestADirectConnectionIsNotToldWhatRuns(t *testing.T) {
	setBusy(t, 20*time.Millisecond, 0)
	release := make(chan struct{})
	c, named := busyHarness(t, false, release)
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	sendEnvelope(t, c, 7)
	time.Sleep(200 * time.Millisecond)
	close(release)
	if n := named(); len(n) != 0 {
		t.Errorf("a direct connection was told %v", n)
	}
}

// As many ids as run fit, a control frame at a time.
func TestBusyPayloadsFitControlFrames(t *testing.T) {
	var ids []int32
	for i := int32(0); i < 100; i++ {
		ids = append(ids, -1-i) // five-byte varints, the longest
	}
	var got []int32
	for _, p := range busyPayloads(ids) {
		if len(p) > cMaxControlPayload {
			t.Fatalf("a payload of %d bytes", len(p))
		}
		got = append(got, busyIDsOf(string(p))...)
	}
	if len(got) != len(ids) {
		t.Fatalf("%d ids came back of %d", len(got), len(ids))
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("id %d came back as %d", ids[i], got[i])
		}
	}
	if busyPayloads(nil) != nil {
		t.Error("a payload for no ids")
	}
}

func setBusy(t *testing.T, every, after time.Duration) {
	t.Helper()
	oldEvery, oldAfter := cBusyEvery, cBusyAfter
	cBusyEvery, cBusyAfter = every, after
	t.Cleanup(func() { cBusyEvery, cBusyAfter = oldEvery, oldAfter })
}
