// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// countingDevice stands in for a device: every connection to it answers
// each request after delay, and it counts its connections, the requests
// each got and the most in flight at once on one connection.
type countingDevice struct {
	url   string
	delay time.Duration

	mu       sync.Mutex
	perConn  []int
	most     int
	gone     chan struct{} // closed: every connection is dropped
	goneOnce sync.Once
}

func newCountingDevice(t *testing.T, delay time.Duration) *countingDevice {
	t.Helper()
	d := &countingDevice{delay: delay, gone: make(chan struct{})}
	upgrader := gorilla.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		d.mu.Lock()
		n := len(d.perConn)
		d.perConn = append(d.perConn, 0)
		d.mu.Unlock()
		go func() { <-d.gone; conn.Close() }()
		var writeMu sync.Mutex
		var inFlight atomic.Int32
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return
			}
			id, _ := envelopeID(frame)
			d.mu.Lock()
			d.perConn[n]++
			d.mu.Unlock()
			go func(id int32) {
				now := inFlight.Add(1)
				d.mu.Lock()
				d.most = max(d.most, int(now))
				d.mu.Unlock()
				time.Sleep(d.delay)
				inFlight.Add(-1)
				b, _ := proto.Marshal(&pb.RespEnvelope{Id: id, ErrorMessage: "span"})
				writeMu.Lock()
				defer writeMu.Unlock()
				conn.WriteMessage(gorilla.BinaryMessage, b)
			}(id)
		}
	}))
	t.Cleanup(srv.Close)
	d.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return d
}

func (d *countingDevice) stats() (conns []int, most int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int(nil), d.perConn...), d.most
}

// drop closes every connection the device has, as a restart does.
func (d *countingDevice) drop() { d.goneOnce.Do(func() { close(d.gone) }) }

// sessionBridge is a bridge whose device "pit.otc" has n connections to
// dev in its pool.
func sessionBridge(t *testing.T, dev *countingDevice, n int) (*Manager, *bridgePool) {
	t.Helper()
	mg := &Manager{bridges: map[string]*bridgePool{}}
	for i := 0; i < n; i++ {
		poolRelayTo(t, mg, "pit.otc", dev.url)
	}
	t.Cleanup(func() { stopOfflineTimer(mg, "pit.otc") })
	return mg, mg.bridges["pit.otc"]
}

func available(pool *bridgePool) int {
	pool.lock.Lock()
	defer pool.lock.Unlock()
	return len(pool.availableConns)
}

// A session spends one of the device's connections for all its requests,
// where each one-off spent one of its own.
func TestASessionKeepsOneConnectionForAllItsRequests(t *testing.T) {
	dev := newCountingDevice(t, 0)
	mg, pool := sessionBridge(t, dev, 4)
	s := mg.OpenSession("pit.otc")
	for i := int32(1); i <= 10; i++ {
		if _, err := s.Forward(envelopeFrame(t, i)); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if left := available(pool); left != 3 {
		t.Errorf("the pool has %d connections left, want 3", left)
	}
	conns, _ := dev.stats()
	used := 0
	for _, n := range conns {
		if n > 0 {
			used++
			if n != 10 {
				t.Errorf("a connection carried %d requests, want all 10", n)
			}
		}
	}
	if used != 1 {
		t.Errorf("%d connections carried requests, want 1", used)
	}
	s.Close()
	waitFor(t, func() bool { return pool.liveCount == 3 }, "the session's connection closed")
}

// Its requests go out together, on the one connection.
func TestASessionsRequestsAreInFlightTogether(t *testing.T) {
	dev := newCountingDevice(t, 100*time.Millisecond)
	mg, _ := sessionBridge(t, dev, 2)
	s := mg.OpenSession("pit.otc")
	defer s.Close()
	var wg sync.WaitGroup
	for i := int32(1); i <= 2; i++ {
		wg.Add(1)
		go func(id int32) {
			defer wg.Done()
			if _, err := s.Forward(envelopeFrame(t, id)); err != nil {
				t.Errorf("request %d: %v", id, err)
			}
		}(i)
	}
	wg.Wait()
	conns, most := dev.stats()
	if most < 2 {
		t.Errorf("at most %d request in flight on the device, want 2", most)
	}
	if len(conns) != 2 || conns[0]+conns[1] != 2 || (conns[0] != 0 && conns[1] != 0) {
		t.Errorf("requests per connection %v, want both on one", conns)
	}
}

// A session's requests count against the device's one-off slots, which the
// static assets share in turn.
func TestASessionsRequestsHoldTheDevicesOneOffSlots(t *testing.T) {
	setVar(t, &cOneOffSlotWait, 100*time.Millisecond)
	dev := newCountingDevice(t, 500*time.Millisecond)
	mg, _ := sessionBridge(t, dev, 6)
	s := mg.OpenSession("pit.otc")
	defer s.Close()
	var wg sync.WaitGroup
	for i := int32(1); i <= cOneOffConcurrent; i++ {
		wg.Add(1)
		go func(id int32) { defer wg.Done(); s.Forward(envelopeFrame(t, id)) }(i)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := mg.ForwardOneOff("pit.otc", envelopeFrame(t, 9)); !errors.Is(err, ErrDeviceBusy) {
		t.Errorf("a one-off past the slots a session holds: %v, want busy", err)
	}
	wg.Wait()
	if _, err := mg.ForwardOneOff("pit.otc", envelopeFrame(t, 10)); err != nil {
		t.Errorf("the slots never came back: %v", err)
	}
}

// A device lends at most cMaxSessionConns connections to sessions; another
// session's requests then go the one-off way, a connection each.
func TestSessionsPastTheDevicesPlacesGoOneOff(t *testing.T) {
	setVar(t, &cMaxSessionConns, 1)
	dev := newCountingDevice(t, 0)
	mg := &Manager{bridges: map[string]*bridgePool{}}
	for i := 0; i < 6; i++ {
		poolRelayTo(t, mg, "cala.otc", dev.url) // a domain no other test's places are counted for
	}
	t.Cleanup(func() { stopOfflineTimer(mg, "cala.otc") })
	pool := mg.bridges["cala.otc"]
	a, b := mg.OpenSession("cala.otc"), mg.OpenSession("cala.otc")
	defer a.Close()
	defer b.Close()
	if _, err := a.Forward(envelopeFrame(t, 1)); err != nil {
		t.Fatal(err)
	}
	for i := int32(1); i <= 3; i++ {
		if _, err := b.Forward(envelopeFrame(t, i)); err != nil {
			t.Fatal(err)
		}
	}
	if left := available(pool); left != 6-1-3 {
		t.Errorf("%d connections left, want 2: one kept by the first session, one per request of the second", left)
	}
	a.Close()
	if _, err := b.Forward(envelopeFrame(t, 4)); err != nil {
		t.Fatal(err)
	}
	b.Forward(envelopeFrame(t, 5))
	if left := available(pool); left != 1 {
		t.Errorf("%d connections left, want 1: the second session kept one once the first gave its place up", left)
	}
}

// A session with nothing asked of it for cSessionIdle gives its connection
// back, and takes another when asked again.
func TestAnIdleSessionGivesItsConnectionBack(t *testing.T) {
	setVar(t, &cSessionIdle, 100*time.Millisecond)
	dev := newCountingDevice(t, 0)
	mg, pool := sessionBridge(t, dev, 3)
	s := mg.OpenSession("pit.otc")
	defer s.Close()
	if _, err := s.Forward(envelopeFrame(t, 1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		pool.lock.Lock()
		defer pool.lock.Unlock()
		return pool.liveCount == 2
	}, "the idle session's connection closed")
	if _, err := s.Forward(envelopeFrame(t, 2)); err != nil {
		t.Fatal(err)
	}
	if left := available(pool); left != 1 {
		t.Errorf("%d connections left, want 1", left)
	}
}

// When the session's connection dies (the device restarted), its next
// request takes another.
func TestASessionTakesAnotherConnectionWhenItsOwnDies(t *testing.T) {
	first := newCountingDevice(t, 0)
	second := newCountingDevice(t, 0)
	mg := &Manager{bridges: map[string]*bridgePool{}}
	poolRelayTo(t, mg, "pit.otc", first.url)
	poolRelayTo(t, mg, "pit.otc", second.url)
	t.Cleanup(func() { stopOfflineTimer(mg, "pit.otc") })
	s := mg.OpenSession("pit.otc")
	defer s.Close()
	if _, err := s.Forward(envelopeFrame(t, 1)); err != nil {
		t.Fatal(err)
	}
	first.drop()
	time.Sleep(50 * time.Millisecond)
	if _, err := s.Forward(envelopeFrame(t, 2)); err != nil {
		t.Fatalf("after its connection died: %v", err)
	}
	if conns, _ := second.stats(); len(conns) != 1 || conns[0] != 1 {
		t.Errorf("the other connection carried %v", conns)
	}
}
