// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// anonServer serves mg's connections (Listen, or serveConnection as a
// bridge connection when bridged) and waits for every handler before the
// test's package vars are put back.
func anonServer(t *testing.T, mg *Manager, bridged bool) string {
	t.Helper()
	var handlers sync.WaitGroup
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		if !bridged {
			mg.Listen(w, r)
			return
		}
		conn, err := mg.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		mg.serveConnection(conn, nil, func() {})
	}))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
		handlers.Wait()
	})
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func shortIdle(t *testing.T, d time.Duration) {
	t.Helper()
	direct, bridged, slots := cAnonIdleDirect, cAnonIdleBridged, directAnon
	cAnonIdleDirect, cAnonIdleBridged = d, d
	directAnon = &anonSlots{perAddr: cMaxAnonPerAddr, total: cMaxAnonDirect}
	// Registered first, so it runs after anonServer's cleanup has waited
	// for its handlers.
	t.Cleanup(func() { cAnonIdleDirect, cAnonIdleBridged, directAnon = direct, bridged, slots })
}

func getProfile(t *testing.T, c *gorilla.Conn) error {
	t.Helper()
	b, _ := proto.Marshal(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqGetProfile{ReqGetProfile: &pb.GetProfile{}}})
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		return err
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := c.ReadMessage()
	return err
}

// closedWithin reports whether the device closes c within d (a read ends
// in an error that isn't the test's own deadline).
func closedWithin(c *gorilla.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	_, _, err := c.ReadMessage()
	if err == nil {
		return false
	}
	if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		return false
	}
	return true
}

func anonManager() *Manager {
	return &Manager{
		upgrader: gorilla.Upgrader{CheckOrigin: originAllowed},
		profile:  profile.InitFromPb(nil, &pb.Profile{Name: "Kim"}),
	}
}

// A socket to the device's own port that never signs in is closed once
// idle; one whose requests keep coming stays open as long as they do.
func TestSessionsThatNeverSignInEndOnceIdle(t *testing.T) {
	const idle = 200 * time.Millisecond
	shortIdle(t, idle)
	url := anonServer(t, anonManager(), false)

	quiet, _, err := gorilla.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Close()
	if !closedWithin(quiet, 20*idle) {
		t.Fatal("a socket that sent nothing was left open")
	}

	busy, _, err := gorilla.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	for range 8 {
		time.Sleep(idle / 2)
		if err := getProfile(t, busy); err != nil {
			t.Fatalf("closed while asking: %v", err)
		}
	}
	if !closedWithin(busy, 20*idle) {
		t.Fatal("not closed once it stopped asking")
	}
}

// A bridge connection waits in the pool for a client as long as it takes;
// once a client is paired on it, the same rule.
func TestBridgeConnectionsEndOnceIdleAfterPairing(t *testing.T) {
	const idle = 200 * time.Millisecond
	shortIdle(t, idle)
	url := anonServer(t, anonManager(), true)

	c, _, err := gorilla.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Idle in the pool: its first client still finds it open.
	time.Sleep(5 * idle)
	if err := getProfile(t, c); err != nil {
		t.Fatalf("the first client request: %v", err)
	}
	if !closedWithin(c, 20*idle) {
		t.Fatal("a paired session that never signed in was left open")
	}
}

// One address holds at most perAddr sockets before signing in: the next is
// refused before the upgrade, and a slot comes back as one closes.
func TestTooManySocketsBeforeSignInFromOneAddress(t *testing.T) {
	shortIdle(t, time.Minute)
	directAnon = &anonSlots{perAddr: 2, total: 100}
	url := anonServer(t, anonManager(), false)

	var open []*gorilla.Conn
	for range 2 {
		c, _, err := gorilla.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		open = append(open, c)
	}
	_, resp, err := gorilla.DefaultDialer.Dial(url, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a third socket: %v, %v; want a 503", err, resp)
	}
	open[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, _, err := gorilla.DefaultDialer.Dial(url, nil)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the slot never came back")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A few addresses keeping their sockets busy before sign-in fill the
// device-wide total: a socket from another address - the owner's page at
// home - still gets in and is answered, and one of theirs is closed for it.
func TestBusyAddressesCantLockOutAnotherBeforeSignIn(t *testing.T) {
	shortIdle(t, time.Minute)
	directAnon = &anonSlots{perAddr: 2, total: 4}
	url := anonServer(t, anonManager(), false)

	from := func(addr string) http.Header {
		// Over loopback, as Tailscale Funnel forwards (clientAddr).
		h := http.Header{}
		h.Set("X-Forwarded-For", addr)
		return h
	}
	var theirs []*gorilla.Conn
	for _, addr := range []string{"2001:db8:1::1", "2001:db8:1::2", "2001:db8:2::1", "2001:db8:2::2"} {
		c, _, err := gorilla.DefaultDialer.Dial(url, from(addr))
		if err != nil {
			t.Fatal(addr, err)
		}
		defer c.Close()
		theirs = append(theirs, c)
	}
	// Asking, as one keeping them open past the idle time would.
	for _, c := range theirs {
		if err := getProfile(t, c); err != nil {
			t.Fatalf("closed while asking: %v", err)
		}
	}
	mine, resp, err := gorilla.DefaultDialer.Dial(url, from("192.168.1.20"))
	if err != nil {
		t.Fatalf("another address was locked out: %v, %v", err, resp)
	}
	defer mine.Close()
	if err := getProfile(t, mine); err != nil {
		t.Fatalf("not answered: %v", err)
	}
	pushed := 0
	for _, c := range theirs {
		if closedWithin(c, 500*time.Millisecond) {
			pushed++
		}
	}
	if pushed != 1 {
		t.Fatalf("%d of theirs closed, want 1", pushed)
	}
}
