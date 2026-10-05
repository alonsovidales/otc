// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"fmt"
	"io"
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
	"github.com/alonsovidales/otc/wsframe"
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
	// Cleanup waits for every handler, so a test's package-var changes
	// (restored with setVar, which must come first) never race one.
	var handlers sync.WaitGroup
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		mg.Listen(w, r)
	}))
	t.Cleanup(func() { srv.Close(); handlers.Wait() })
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

// setVar sets *p to v until the test's cleanups have run.
func setVar[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
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

// A refused registration, and a first frame that isn't a protobuf, close
// the socket: nothing reads it afterwards, and it used to sit open.
func TestRefusedRegistrationAndBadFirstFrameCloseTheSocket(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `owner_uuid`, `secret` from `devices` where `domain` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"owner_uuid", "secret"}).AddRow("someone-else", "other"))
	mock.ExpectExec("insert into `auth_events`").WillReturnResult(sqlmock.NewResult(1, 1))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, _ := newTestBridge(t, mg)

	c := dial()
	if err := c.WriteMessage(gorilla.BinaryMessage, registerFrame(t, "pit.otc", "owner-uuid", "secret")); err != nil {
		t.Fatal(err)
	}
	if resp := readResp(t, c); resp.ErrorMessage != "Invalid Secret" {
		t.Fatalf("got %q, want Invalid Secret", resp.ErrorMessage)
	}
	expectClosed(t, c)

	c = dial()
	if err := c.WriteMessage(gorilla.BinaryMessage, []byte{0xff, 0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, c)
}

func expectClosed(t *testing.T, c *gorilla.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := c.ReadMessage()
	if err == nil {
		t.Fatal("got a message, want the socket closed")
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("the socket was left open")
	}
}

// expectValidDevice answers IsValidDevice with owner/secret on record.
func expectValidDevice(mock sqlmock.Sqlmock, owner, secret string) {
	mock.ExpectQuery("select `owner_uuid`, `secret` from `devices` where `domain` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"owner_uuid", "secret"}).AddRow(owner, secret))
}

// A registered connection gets its ack as its first frame, is pooled and
// counted; and one that dies right away is counted out again.
func TestRegistrationAcksThenPoolsAndCountsTheRelay(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	expectValidDevice(mock, "owner-uuid", "secret")

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, _ := newTestBridge(t, mg)
	c := dial()
	if err := c.WriteMessage(gorilla.BinaryMessage, registerFrame(t, "pit.otc", "owner-uuid", "secret")); err != nil {
		t.Fatal(err)
	}
	resp := readResp(t, c)
	if ack, ok := resp.Payload.(*pb.RespEnvelope_RespBridgeAckOnboard); !ok || !ack.RespBridgeAckOnboard.Ok {
		t.Fatalf("first frame is %T, want the registration ack", resp.Payload)
	}
	pool := waitForPool(t, mg, "pit.otc", func(p *bridgePool) bool { return len(p.availableConns) == 1 && p.liveCount == 1 })

	c.Close()
	waitForPool(t, mg, "pit.otc", func(p *bridgePool) bool { return len(p.availableConns) == 0 && p.liveCount == 0 })
	pool.lock.Lock()
	if pool.offlineTimer != nil {
		pool.offlineTimer.Stop()
	}
	pool.lock.Unlock()
}

func waitForPool(t *testing.T, mg *Manager, domain string, cond func(*bridgePool) bool) *bridgePool {
	t.Helper()
	for i := 0; i < 200; i++ {
		mg.bridgesMu.RLock()
		p := mg.bridges[domain]
		mg.bridgesMu.RUnlock()
		if p != nil {
			p.lock.Lock()
			ok := cond(p)
			p.lock.Unlock()
			if ok {
				return p
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pool for %s never reached the expected state", domain)
	return nil
}

// The device decodes with proto.Unmarshal, where a repeated id's last
// value wins; the bridge must wait on that same id.
func TestEnvelopeIDTakesTheLastIDLikeProtobuf(t *testing.T) {
	payload, _ := proto.Marshal(&pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Key: []byte("k")}}})
	twice := append([]byte{0x08, 0x05, 0x08, 0x07}, payload...)
	if id, err := envelopeID(twice); err != nil || id != 7 {
		t.Errorf("envelopeID = %d, %v; want 7 (the last id)", id, err)
	}
	var env pb.ReqEnvelope
	if err := proto.Unmarshal(twice, &env); err != nil || env.Id != 7 {
		t.Fatalf("proto.Unmarshal disagrees: %d, %v", env.Id, err)
	}
	// One id and a malformed tail: still that id, as before.
	if id, err := envelopeID([]byte{0x08, 0x05, 0xff, 0xff}); err != nil || id != 5 {
		t.Errorf("envelopeID = %d, %v; want 5", id, err)
	}
}

// A client's first request may spend at most cPairMaxAttempts of the
// device's connections, then gets the unreachable answer.
func TestPairingCannotDrainThePool(t *testing.T) {
	setVar(t, &cForwardTimeout, 50*time.Millisecond)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, host := newTestBridge(t, mg)
	const poolSize = 6
	pool := &bridgePool{lock: new(sync.Mutex)}
	for i := 0; i < poolSize; i++ {
		srv, wsURL := newEchoDeviceServer(t, func(int32) time.Duration { return time.Hour })
		t.Cleanup(srv.Close)
		pool.availableConns = append(pool.availableConns, dialRelay(t, wsURL))
	}
	mg.bridges[host] = pool

	c := dial()
	if err := c.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, 1)); err != nil {
		t.Fatal(err)
	}
	resp := readResp(t, c)
	if ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck); !ok || ack.RespAck.Code != cCodeDeviceUnreachable {
		t.Fatalf("got %T, want the unreachable RespAck", resp.Payload)
	}
	pool.lock.Lock()
	left := len(pool.availableConns)
	pool.lock.Unlock()
	if left != poolSize-cPairMaxAttempts {
		t.Errorf("pool has %d connections left, want %d", left, poolSize-cPairMaxAttempts)
	}
}

// A socket that never sends anything, or stalls inside its first frame,
// is closed after cUnpairedReadTimeout; a paired client may go quiet for
// as long as it likes.
func TestUnpairedConnectionsTimeOutPairedOnesDoNot(t *testing.T) {
	setVar(t, &cUnpairedReadTimeout, 200*time.Millisecond)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, host := newTestBridge(t, mg)

	expectClosed(t, dial()) // says nothing at all

	// The start of a message (more than the client's write buffer, so a
	// first fragment goes out), then nothing.
	stalled := dial()
	w, err := stalled.NextWriter(gorilla.BinaryMessage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 10000)); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, stalled)

	srv, wsURL := newEchoDeviceServer(t, func(int32) time.Duration { return 0 })
	t.Cleanup(srv.Close)
	mg.bridges[host] = &bridgePool{lock: new(sync.Mutex), availableConns: []*deviceRelay{dialRelay(t, wsURL)}}
	paired := dial()
	for i, wait := range []time.Duration{0, 3 * cUnpairedReadTimeout} {
		time.Sleep(wait)
		if err := paired.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, int32(i+1))); err != nil {
			t.Fatal(err)
		}
		if resp := readResp(t, paired); resp.Id != int32(i+1) || resp.Error {
			t.Fatalf("request %d: got id %d error %v %q", i+1, resp.Id, resp.Error, resp.ErrorMessage)
		}
	}
}

// newBigReplyDeviceServer answers every request with a reply of size bytes.
func newBigReplyDeviceServer(t *testing.T, size int) string {
	t.Helper()
	big := strings.Repeat("x", size)
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
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// A paired client that stops reading is hung up on once a reply makes no
// progress for cClientWriteStall, which frees its device connection;
// before, the writer blocked forever and everything behind it stayed held.
func TestClientThatStopsReadingIsHungUpOn(t *testing.T) {
	setVar(t, &cClientWriteStall, 300*time.Millisecond)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, host := newTestBridge(t, mg)
	died := make(chan struct{})
	relay := dialRelayWithOnDeath(t, newBigReplyDeviceServer(t, 4<<20), func() { close(died) })
	mg.bridges[host] = &bridgePool{lock: new(sync.Mutex), availableConns: []*deviceRelay{relay}}

	c := dial()
	for i := int32(1); i <= 16; i++ { // ~64 MB of replies, far more than socket buffers hold
		if err := c.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, i)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-died:
	case <-time.After(10 * time.Second):
		t.Fatal("the device connection is still held by a client that stopped reading")
	}
}

// A large reply reaches a client that reads, fragmented or not.
func TestLargeReplyReachesTheClientWhole(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, host := newTestBridge(t, mg)
	relay := dialRelay(t, newBigReplyDeviceServer(t, 3<<20))
	mg.bridges[host] = &bridgePool{lock: new(sync.Mutex), availableConns: []*deviceRelay{relay}}

	c := dial()
	for i := int32(1); i <= 2; i++ { // the pairing reply, then a relayed one
		if err := c.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, i)); err != nil {
			t.Fatal(err)
		}
		if resp := readResp(t, c); resp.Id != i || len(resp.ErrorMessage) != 3<<20 {
			t.Fatalf("reply %d: id %d, %d bytes", i, resp.Id, len(resp.ErrorMessage))
		}
	}
}

// A request that takes longer than cPongWait to reach a slow-reading
// device must not get its relay declared dead: the keepalive ping goes out
// between the request's frames and the device answers it while reading.
// The relay is the server side of the socket, as on the bridge, where
// gorilla otherwise writes a whole message as one frame.
func TestLargeRequestDoesNotStarveTheKeepalive(t *testing.T) {
	setVar(t, &cPongWait, 800*time.Millisecond)
	setVar(t, &cPingPeriod, 100*time.Millisecond)
	const size = 32 << 20

	var died atomic.Int32
	relays := make(chan *deviceRelay, 1)
	upgrader := gorilla.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		relays <- newDeviceRelay(conn, func() { died.Add(1) })
	}))
	t.Cleanup(srv.Close)

	device, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { device.Close() })
	relay := <-relays
	t.Cleanup(func() { relay.Close() })

	go func() { // the device: reads the request slowly, then answers
		device.SetReadLimit(2 * size)
		_, rd, err := device.NextReader()
		if err != nil {
			return
		}
		var msg []byte
		buf := make([]byte, 256<<10)
		for {
			n, err := io.ReadFull(rd, buf)
			msg = append(msg, buf[:n]...)
			if err != nil {
				break
			}
			time.Sleep(10 * time.Millisecond) // ~25 MB/s: the whole request takes over a second
		}
		id, _ := envelopeID(msg)
		resp, _ := proto.Marshal(&pb.RespEnvelope{Id: id, ErrorMessage: fmt.Sprint(len(msg))})
		device.WriteMessage(gorilla.BinaryMessage, resp)
		device.ReadMessage() // answers pings until the relay closes
	}()

	req, _ := proto.Marshal(&pb.ReqEnvelope{Id: 9, Payload: &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Key: make([]byte, size)}}})
	start := time.Now()
	respFrame, release, err := relay.forward(req)
	if err != nil {
		t.Fatalf("forward failed after %v: %v", time.Since(start), err)
	}
	defer release()
	if time.Since(start) < cPongWait {
		t.Logf("the request took only %v; the test proves nothing on this machine", time.Since(start))
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(respFrame, &resp); err != nil || resp.Id != 9 || resp.ErrorMessage != fmt.Sprint(len(req)) {
		t.Fatalf("reply %v %q, want id 9 and %d bytes received", err, resp.ErrorMessage, len(req))
	}
	if died.Load() != 0 {
		t.Fatal("the relay was declared dead during the upload")
	}
}

// Device replies are read within a budget, and a reply that comes after
// its request gave up gives its share back: leaked, the next large reply
// would wait for room that never comes (wsframe's cWait, then the relay
// dies).
func TestLateReplyGivesItsBudgetBack(t *testing.T) {
	const size = 6 << 20 // past wsframe's free 4 MiB: reserves one 4 MiB step
	big := strings.Repeat("x", size)
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
			id, _ := envelopeID(frame)
			time.Sleep(200 * time.Millisecond)
			resp, _ := proto.Marshal(&pb.RespEnvelope{Id: id, ErrorMessage: big})
			if conn.WriteMessage(gorilla.BinaryMessage, resp) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	conn, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	relay := newIdleDeviceRelay(conn, nil)
	relay.budget = wsframe.NewBudget(4 << 20) // room for one such reply
	relay.start()
	t.Cleanup(func() { relay.Close() })

	if _, _, err := relay.forwardWithTimeout(envelopeFrame(t, 1), 50*time.Millisecond); err == nil {
		t.Fatal("the first request should have timed out")
	}
	time.Sleep(500 * time.Millisecond) // its reply arrives with nobody waiting

	start := time.Now()
	resp, release, err := relay.forwardWithTimeout(envelopeFrame(t, 2), 5*time.Second)
	if err != nil {
		t.Fatalf("the next large reply never got room (budget leaked?): %v after %v", err, time.Since(start))
	}
	release()
	if len(resp) < size {
		t.Fatalf("short reply: %d bytes", len(resp))
	}
}
