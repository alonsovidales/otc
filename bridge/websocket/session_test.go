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

// Closing a session - its client hung up - ends its requests at once: on
// its own connection, on one taken for a request alone and waiting for a
// slot. One the device was slow on held its slot until the device answered.
func TestClosingASessionEndsItsRequestsAtOnce(t *testing.T) {
	setVar(t, &cMaxSessionConns, 1)
	dev := newCountingDevice(t, time.Hour)
	mg := &Manager{bridges: map[string]*bridgePool{}}
	for i := 0; i < 6; i++ {
		poolRelayTo(t, mg, "ria.otc", dev.url) // a domain no other test's places are counted for
	}
	t.Cleanup(func() { stopOfflineTimer(mg, "ria.otc") })
	kept, loose := mg.OpenSession("ria.otc"), mg.OpenSession("ria.otc")
	results := make(chan error, 4)
	ask := func(s *Session, id int32) {
		go func() { _, err := s.Forward(envelopeFrame(t, id)); results <- err }()
		time.Sleep(100 * time.Millisecond)
	}
	ask(kept, 1)  // takes the device's one session place
	ask(loose, 2) // a connection of its own
	ask(loose, 3) // the last slot
	ask(loose, 4) // waits for a slot
	if n := len(mg.oneOffSlot("ria.otc")); n != cOneOffConcurrent {
		t.Fatalf("%d slots taken, want all %d", n, cOneOffConcurrent)
	}
	kept.Close()
	loose.Close()
	for i := 0; i < 4; i++ {
		select {
		case err := <-results:
			if err == nil {
				t.Error("a closed session's request got a reply")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a closed session's request went on waiting")
		}
	}
	if n := len(mg.oneOffSlot("ria.otc")); n != 0 {
		t.Errorf("%d slots still taken", n)
	}
}

// serialDevice stands in for a device on a slow uplink that predates the
// busy pongs: its replies go out one after another, each taking send, and
// it answers every request but those forget names. It counts the requests.
func serialDevice(t *testing.T, send time.Duration, forget func(int32) bool) (url string, requests *atomic.Int32) {
	t.Helper()
	requests = &atomic.Int32{}
	upgrader := gorilla.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		queue := make(chan int32, 64)
		defer close(queue)
		go func() {
			for id := range queue {
				time.Sleep(send)
				b, _ := proto.Marshal(&pb.RespEnvelope{Id: id, ErrorMessage: "span"})
				conn.WriteMessage(gorilla.BinaryMessage, b)
			}
		}()
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return
			}
			requests.Add(1)
			if id, _ := envelopeID(frame); forget == nil || !forget(id) {
				queue <- id
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), requests
}

// A video's span asked for ahead waits on the connection for the reply in
// front of it as well as its own; its clock starts again when that one has
// arrived. It ran from its own write, so on a line slower than about two
// spans per cOneOffForwardTimeout a span the one-at-a-time loop delivered
// timed out, dropped the connection, and was asked again on a cold one.
func TestASpanQueuedBehindAnotherIsWaitedForFromItsTurn(t *testing.T) {
	setVar(t, &cOneOffForwardTimeout, 450*time.Millisecond)
	url, requests := serialDevice(t, 280*time.Millisecond, nil)
	mg := &Manager{bridges: map[string]*bridgePool{}}
	for i := 0; i < 3; i++ {
		poolRelayTo(t, mg, "pit.otc", url)
	}
	t.Cleanup(func() { stopOfflineTimer(mg, "pit.otc") })
	s := mg.OpenSession("pit.otc")
	defer s.Close()
	var wg sync.WaitGroup
	for i := int32(1); i <= 2; i++ {
		wg.Add(1)
		go func(id int32) {
			defer wg.Done()
			if _, err := s.Forward(envelopeFrame(t, id)); err != nil {
				t.Errorf("span %d: %v", id, err)
			}
		}(i)
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	if n := requests.Load(); n != 2 {
		t.Errorf("the device was asked %d times for 2 spans: one was asked again", n)
	}
}

// Only the replies of requests sent before it start a request's clock
// again: one the device forgot still times out, however many replies of
// requests sent after it arrive.
func TestRepliesOfLaterRequestsDontKeepAForgottenOneWaiting(t *testing.T) {
	url, _ := serialDevice(t, 50*time.Millisecond, func(id int32) bool { return id == 1 })
	relay := dialRelay(t, url)
	defer relay.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for id := int32(2); ; id++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, release, err := relay.forwardWithTimeout(envelopeFrame(t, id), time.Second, time.Hour); err == nil {
				release()
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	if _, _, err := relay.forwardWithTimeout(envelopeFrame(t, 1), 300*time.Millisecond, 3*time.Second); err == nil {
		t.Fatal("a request the device forgot was answered")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("a request the device forgot was waited for %v, kept by later replies", d)
	}
}
