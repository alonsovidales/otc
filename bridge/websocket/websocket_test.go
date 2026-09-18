// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
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

// newSilentAfterFirstDeviceServer starts a test server standing in for a
// device that goes silent at the network level - no close frame, no FIN,
// just nothing comes back any more (a cut cable, killed wifi, a powered-off
// Pi). goSilent(), once called, makes the server swallow every ping it
// receives instead of auto-replying with a pong, while leaving the
// underlying connection technically open - the exact failure mode a
// graceful shutdown (which sends a real close, and which every other test
// in this file exercises) doesn't reproduce.
func newSilentAfterFirstDeviceServer(t *testing.T) (srv *httptest.Server, wsURL string, goSilent func()) {
	t.Helper()
	upgrader := gorilla.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	silent := make(chan struct{})
	var once sync.Once
	goSilent = func() { once.Do(func() { close(silent) }) }

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetPingHandler(func(appData string) error {
			select {
			case <-silent:
				return nil // swallow it - no pong, simulating total silence
			default:
				return conn.WriteControl(gorilla.PongMessage, []byte(appData), time.Now().Add(time.Second))
			}
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	wsURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	return srv, wsURL, goSilent
}

func dialRelay(t *testing.T, wsURL string) *deviceRelay {
	t.Helper()
	return dialRelayWithOnDeath(t, wsURL, nil)
}

func dialRelayWithOnDeath(t *testing.T, wsURL string, onDeath func()) *deviceRelay {
	t.Helper()
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialing test device server: %v", err)
	}
	return newDeviceRelay(conn, onDeath)
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

// onDeath (issue #62) is what tells bridgePool.liveCount a device
// connection is gone - it must fire, and must fire exactly once, whenever
// the underlying connection dies, however that happens (explicit Close()
// here; a genuine network failure hits the exact same failAll() path).
func TestDeviceRelayOnDeathFiresExactlyOnceWhenConnectionDies(t *testing.T) {
	srv, wsURL := newEchoDeviceServer(t, func(int32) time.Duration { return time.Hour })
	defer srv.Close()

	var calls int32
	relay := dialRelayWithOnDeath(t, wsURL, func() { atomic.AddInt32(&calls, 1) })
	relay.Close()

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("onDeath called %d times, want exactly 1", got)
	}
}

// The bug actually reported in the field: disconnecting a device at the
// network level (no clean close) for several minutes never triggered an
// alert. Every other death-detection test in this file kills the
// connection with a real close, which ReadMessage() already errors out of
// on its own with no help needed - that's not what happened. This
// reproduces the real failure mode: the device goes silent (no more pongs)
// while its socket stays technically open, and only the ping/pong
// keepalive's read-deadline timeout (cPongWait/cPingPeriod) can ever
// notice.
func TestDeviceRelayDetectsSilentNetworkDeathViaPingPongTimeout(t *testing.T) {
	origPongWait, origPingPeriod := cPongWait, cPingPeriod
	cPongWait = 100 * time.Millisecond
	cPingPeriod = 30 * time.Millisecond
	defer func() { cPongWait, cPingPeriod = origPongWait, origPingPeriod }()

	srv, wsURL, goSilent := newSilentAfterFirstDeviceServer(t)
	defer srv.Close()

	var calls int32
	relay := dialRelayWithOnDeath(t, wsURL, func() { atomic.AddInt32(&calls, 1) })
	defer relay.Close()

	// Let a few healthy ping/pong cycles pass first - the keepalive must
	// never kill a connection that's actually still answering.
	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("onDeath fired before the connection ever went silent")
	}

	goSilent()

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("onDeath called %d times after the connection went silent (no close, just no more pongs) - want exactly 1", got)
	}
}

// The actual production bug: a relay that died via the ping/pong timeout
// (not an explicit Close()) used to leave its underlying socket
// technically still open - if the bridge's pool later picked that same
// relay again (exactly what happens when a dead entry sits unevicted in
// availableConns) and called forward() on it, nothing would ever answer
// the waiter it registered, since this relay's one and only readLoop had
// already returned for good. That hung the caller (and the real client
// behind it) forever, with no error - reproduced here directly: forward()
// after a ping/pong death must fail promptly, not hang.
func TestDeviceRelayForwardFailsPromptlyAfterPingPongTimeoutDeath(t *testing.T) {
	origPongWait, origPingPeriod, origForwardTimeout := cPongWait, cPingPeriod, cForwardTimeout
	cPongWait = 100 * time.Millisecond
	cPingPeriod = 30 * time.Millisecond
	cForwardTimeout = 2 * time.Second
	defer func() { cPongWait, cPingPeriod, cForwardTimeout = origPongWait, origPingPeriod, origForwardTimeout }()

	srv, wsURL, goSilent := newSilentAfterFirstDeviceServer(t)
	defer srv.Close()

	var calls int32
	relay := dialRelayWithOnDeath(t, wsURL, func() { atomic.AddInt32(&calls, 1) })
	defer relay.Close()

	goSilent()
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatal("relay never died via the ping/pong timeout - test setup is broken")
	}

	// This is the exact scenario: the pool still holds this now-dead
	// relay (nothing evicts it from availableConns just because it died
	// while idle) and picks it for a real request.
	done := make(chan error, 1)
	go func() {
		_, err := relay.forward(envelopeFrame(t, 99))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected an error forwarding through an already-dead relay, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forward() hung instead of failing promptly - the underlying socket was never actually closed on death")
	}
}

// The exact design (issue #62): "if the device connections goes from > 1
// to 0 then start a go routine with a count down that will send the
// notification if it takes more than 1 min to go back to 1". This pins
// the zero-liveCount side: once the last live connection dies, a countdown
// starts, and if nothing reconnects before it elapses, the offline alert
// actually goes out (exercised here via the real push.Init/Notify flow,
// backed by a mocked DB - the send itself no-ops because no APNs/web push
// registrations exist).
func TestOfflineCountdownFiresAlertWhenStillDownAfterGrace(t *testing.T) {
	origGrace := cOfflineAlertGrace
	cOfflineAlertGrace = 30 * time.Millisecond
	defer func() { cOfflineAlertGrace = origGrace }()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `vapid_public_key`, `vapid_private_key` from `push_registrations` where `domain` = \\?").
		WithArgs("pit.otc").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("insert into `push_registrations`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("select `endpoint`, `p256dh`, `auth` from `push_web_subs` where `domain` = \\?").
		WithArgs("pit.otc").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "p256dh", "auth"}))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: make(map[string]*bridgePool)}
	pool := &bridgePool{lock: new(sync.Mutex)}
	mg.bridges["pit.otc"] = pool

	pool.lock.Lock()
	mg.onDeviceConnectionRegistered("pit.otc", pool) // liveCount 0 -> 1
	pool.lock.Unlock()

	mg.onDeviceConnectionDied("pit.otc") // liveCount 1 -> 0, starts the countdown

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mock.ExpectationsWereMet() == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected the offline alert's push flow to have run once the grace period elapsed: %v", err)
	}
}

// The other half of the same design: reconnecting within the grace window
// must cancel the pending countdown so no alert ever fires for a device
// that only briefly touched zero live connections.
func TestOnDeviceConnectionRegisteredCancelsPendingOfflineCountdown(t *testing.T) {
	origGrace := cOfflineAlertGrace
	cOfflineAlertGrace = 50 * time.Millisecond
	defer func() { cOfflineAlertGrace = origGrace }()

	// mg.dao is deliberately left nil: if cancellation is broken and the
	// countdown fires anyway, sendOfflineAlert's domainPushStorage would
	// dereference a nil *dao.Dao and panic - the strictest possible check
	// that the alert path never runs after a reconnect.
	pool := &bridgePool{lock: new(sync.Mutex)}
	mg := &Manager{bridges: map[string]*bridgePool{"pit.otc": pool}}

	pool.lock.Lock()
	mg.onDeviceConnectionRegistered("pit.otc", pool)
	pool.lock.Unlock()

	mg.onDeviceConnectionDied("pit.otc")

	pool.lock.Lock()
	hasPendingTimer := pool.offlineTimer != nil
	pool.lock.Unlock()
	if !hasPendingTimer {
		t.Fatal("expected a pending offline timer once liveCount hit zero")
	}

	pool.lock.Lock()
	mg.onDeviceConnectionRegistered("pit.otc", pool)
	stillPending := pool.offlineTimer != nil
	pool.lock.Unlock()
	if stillPending {
		t.Error("expected the offline timer to be cancelled once the device reconnected")
	}

	// Outlive the original grace window to give a wrongly-still-running
	// timer a chance to fire (and panic, per the doc comment above).
	time.Sleep(150 * time.Millisecond)
}

// Issue #95: ForwardOneOff is what proxyStaticAsset (bridge/api) calls for
// every static asset request - this is its own one-shot round trip to a
// registered device, distinct from Listen's default pass-through case
// (which keeps a picked connection paired for a whole browser session).
func TestForwardOneOffReturnsDeviceResponse(t *testing.T) {
	srv, wsURL := newEchoDeviceServer(t, func(int32) time.Duration { return 0 })
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectExec("insert into `device_metrics`").
		WithArgs("cala.otc", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	relay := dialRelay(t, wsURL)
	mg := &Manager{
		dao:     dao.NewWithDB(db),
		bridges: map[string]*bridgePool{"cala.otc": {lock: new(sync.Mutex), availableConns: []*deviceRelay{relay}}},
	}

	respFrame, err := mg.ForwardOneOff("cala.otc", envelopeFrame(t, 7))
	if err != nil {
		t.Fatalf("ForwardOneOff: %v", err)
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(respFrame, &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}
	if resp.ErrorMessage != "reply-to-7" {
		t.Errorf("expected the echoed device response, got %q", resp.ErrorMessage)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected device activity to be recorded: %v", err)
	}
}

func TestForwardOneOffFailsWhenDomainNotRegistered(t *testing.T) {
	mg := &Manager{bridges: map[string]*bridgePool{}}

	if _, err := mg.ForwardOneOff("unknown.otc", envelopeFrame(t, 1)); err == nil {
		t.Fatal("expected an error for a domain with no registered device")
	}
}

func TestForwardOneOffFailsWhenPoolIsEmpty(t *testing.T) {
	mg := &Manager{bridges: map[string]*bridgePool{
		"cala.otc": {lock: new(sync.Mutex)}, // registered, but nothing available right now
	}}

	if _, err := mg.ForwardOneOff("cala.otc", envelopeFrame(t, 1)); err == nil {
		t.Fatal("expected an error when the pool has no available connections")
	}
}

// A connection ForwardOneOff picks is always spent afterward - unlike
// Listen's default case, nothing keeps it around for a follow-up request,
// since a plain HTTP handler (proxyStaticAsset) has no persistent session
// of its own to keep it for.
func TestForwardOneOffConsumesTheConnectionItUses(t *testing.T) {
	srv, wsURL := newEchoDeviceServer(t, func(int32) time.Duration { return 0 })
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectExec("insert into `device_metrics`").WillReturnResult(sqlmock.NewResult(1, 1))

	relay := dialRelay(t, wsURL)
	pool := &bridgePool{lock: new(sync.Mutex), availableConns: []*deviceRelay{relay}}
	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{"cala.otc": pool}}

	if _, err := mg.ForwardOneOff("cala.otc", envelopeFrame(t, 1)); err != nil {
		t.Fatalf("ForwardOneOff: %v", err)
	}

	pool.lock.Lock()
	remaining := len(pool.availableConns)
	pool.lock.Unlock()
	if remaining != 0 {
		t.Errorf("expected the used connection to be removed from the pool, %d still available", remaining)
	}
}

// Issue #95: reproduced live against a device whose pool had accumulated
// stale entries (past process restarts over one long session never got
// cleaned out of availableConns) - the first candidate ForwardOneOff
// tries never answers at all, and it must give up on it and try the next
// one quickly rather than waiting anywhere near cForwardTimeout's 90s.
func TestForwardOneOffSkipsAStaleCandidateQuickly(t *testing.T) {
	origTimeout := cOneOffForwardTimeout
	cOneOffForwardTimeout = 100 * time.Millisecond
	defer func() { cOneOffForwardTimeout = origTimeout }()

	staleSrv, staleURL, _ := newSilentAfterFirstDeviceServer(t)
	defer staleSrv.Close()
	goodSrv, goodURL := newEchoDeviceServer(t, func(int32) time.Duration { return 0 })
	defer goodSrv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectExec("insert into `device_metrics`").WillReturnResult(sqlmock.NewResult(1, 1))

	pool := &bridgePool{
		lock: new(sync.Mutex),
		availableConns: []*deviceRelay{
			dialRelay(t, staleURL), // never answers - must be skipped
			dialRelay(t, goodURL),  // answers immediately
		},
	}
	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{"cala.otc": pool}}

	start := time.Now()
	respFrame, err := mg.ForwardOneOff("cala.otc", envelopeFrame(t, 3))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ForwardOneOff: %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("expected the stale candidate to be skipped in well under a second, took %v", elapsed)
	}

	var resp pb.RespEnvelope
	if err := proto.Unmarshal(respFrame, &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}
	if resp.ErrorMessage != "reply-to-3" {
		t.Errorf("expected the good candidate's echoed response, got %q", resp.ErrorMessage)
	}
}

// Issue #93: a disabled domain's own request must get a clear explanation
// - checked before the pool lookup even runs, so this holds regardless of
// whether that domain happens to have connections available (a disabled
// user's process is normally stopped and wouldn't, but a stale entry
// lingering in the pool - see issue #95's own testing - must not let a
// disabled account slip through anyway).
func TestClientRequestToDisabledDomainGetsAClearError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(true))

	mg := &Manager{
		dao:      dao.NewWithDB(db),
		bridges:  map[string]*bridgePool{},
		upgrader: gorilla.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}
	srv := httptest.NewServer(http.HandlerFunc(mg.Listen))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialing bridge: %v", err)
	}
	defer conn.Close()

	// Any non-registration request - the disabled check runs in the
	// default pass-through case, before any pool lookup.
	if err := conn.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, 1)); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}
	if !resp.Error || resp.ErrorMessage != "This account has been disabled." {
		t.Errorf("expected a clear disabled-account error, got Error=%v ErrorMessage=%q", resp.Error, resp.ErrorMessage)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}
