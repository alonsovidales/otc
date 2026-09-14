// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// newEchoDeviceServer starts a test server standing in for a device: it
// replies to every request with a RespEnvelope carrying the same id, error
// text describing which request it was, after waiting `delay(id)` first —
// letting tests control which requests finish before which.
func newEchoDeviceServer(t *testing.T, delay func(id int32) time.Duration) (*httptest.Server, string) {
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
			var env pb.ReqEnvelope
			if err := proto.Unmarshal(frame, &env); err != nil {
				return
			}
			go func(id int32) {
				time.Sleep(delay(id))
				resp := &pb.RespEnvelope{Id: id, ErrorMessage: fmt.Sprintf("reply-to-%d", id)}
				respBin, _ := proto.Marshal(resp)
				writeMu.Lock()
				defer writeMu.Unlock()
				_ = conn.WriteMessage(gorilla.BinaryMessage, respBin)
			}(env.Id)
		}
	}))
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return srv, wsURL
}

func dialRelay(t *testing.T, wsURL string) *deviceRelay {
	t.Helper()
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialing test device server: %v", err)
	}
	return newDeviceRelay(conn)
}

func envelopeFrame(t *testing.T, id int32) []byte {
	t.Helper()
	frame, err := proto.Marshal(&pb.ReqEnvelope{Id: id})
	if err != nil {
		t.Fatalf("marshaling request envelope: %v", err)
	}
	return frame
}

// This is the exact scenario the relay exists to fix: a slow request and a
// fast one in flight on the same device connection at once. The device
// here deliberately answers the *first* request (id 1) last and the
// *second* (id 2) first — if forward() were still matching responses by
// arrival order rather than envelope id, request 1's caller would
// incorrectly get request 2's reply back (or vice versa). Getting each
// caller its own matching id back, in whichever order the device actually
// answers, is what makes it safe for the bridge to stop forcing requests
// through one at a time (see deviceRelay's doc comment).
func TestDeviceRelayForwardMatchesResponsesByIdNotArrivalOrder(t *testing.T) {
	srv, wsURL := newEchoDeviceServer(t, func(id int32) time.Duration {
		if id == 1 {
			return 100 * time.Millisecond
		}
		return 5 * time.Millisecond
	})
	defer srv.Close()
	relay := dialRelay(t, wsURL)
	defer relay.Close()

	var wg sync.WaitGroup
	results := make(map[int32]*pb.RespEnvelope, 2)
	var mu sync.Mutex
	for _, id := range []int32{1, 2} {
		wg.Add(1)
		go func(id int32) {
			defer wg.Done()
			respFrame, err := relay.forward(envelopeFrame(t, id))
			if err != nil {
				t.Errorf("forward(%d): unexpected error: %v", id, err)
				return
			}
			var resp pb.RespEnvelope
			if err := proto.Unmarshal(respFrame, &resp); err != nil {
				t.Errorf("forward(%d): unmarshaling response: %v", id, err)
				return
			}
			mu.Lock()
			results[id] = &resp
			mu.Unlock()
		}(id)
	}
	wg.Wait()

	for _, id := range []int32{1, 2} {
		resp, ok := results[id]
		if !ok {
			t.Fatalf("no result recorded for request %d", id)
		}
		if resp.Id != id {
			t.Errorf("request %d got back a response for id %d instead", id, resp.Id)
		}
		want := fmt.Sprintf("reply-to-%d", id)
		if resp.ErrorMessage != want {
			t.Errorf("request %d got payload %q, want %q", id, resp.ErrorMessage, want)
		}
	}
}

// A device connection dying mid-flight must fail every request still
// waiting on it, not leave them blocked on their response channel forever.
func TestDeviceRelayFailAllUnblocksPendingForwardsWhenConnectionDies(t *testing.T) {
	srv, wsURL := newEchoDeviceServer(t, func(int32) time.Duration { return time.Hour })
	relay := dialRelay(t, wsURL)
	defer relay.Close()

	done := make(chan error, 1)
	go func() {
		_, err := relay.forward(envelopeFrame(t, 1))
		done <- err
	}()

	// Give forward() time to register its waiter before pulling the rug
	// out from under it.
	time.Sleep(20 * time.Millisecond)
	srv.Close()
	relay.conn.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected an error once the device connection died mid-flight")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forward() never returned after the device connection died - a pending request is stuck forever")
	}
}
