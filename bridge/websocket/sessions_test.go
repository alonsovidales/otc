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

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// newSignInDeviceServer stands in for a device that signs a connection in
// with the session token "good" (an Ack with ok, as every release
// answers), refuses any other token, and answers everything else with an
// echo after delay(id).
func newSignInDeviceServer(t *testing.T, delay func(id int32) time.Duration) string {
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
			go func(env *pb.ReqEnvelope) {
				resp := &pb.RespEnvelope{Id: env.Id, ErrorMessage: fmt.Sprintf("reply-to-%d", env.Id)}
				if tok, ok := env.Payload.(*pb.ReqEnvelope_ReqAuthWithToken); ok {
					resp = &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: tok.ReqAuthWithToken.Token == "good"}}}
				} else if delay != nil {
					time.Sleep(delay(env.Id))
				}
				b, _ := proto.Marshal(resp)
				writeMu.Lock()
				defer writeMu.Unlock()
				_ = conn.WriteMessage(gorilla.BinaryMessage, b)
			}(&env)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// poolRelayTo registers a relay to the device at wsURL in domain's pool,
// as ReqBridgeRegister does.
func poolRelayTo(t *testing.T, mg *Manager, domain, wsURL string) *deviceRelay {
	t.Helper()
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	var relay *deviceRelay
	relay = newIdleDeviceRelay(conn, func() { mg.onDeviceConnectionDied(domain, relay) })
	relay.owner = "owner"
	t.Cleanup(func() { relay.Close() })
	mg.bridgesMu.Lock()
	pool, ok := mg.bridges[domain]
	if !ok {
		pool = &bridgePool{lock: new(sync.Mutex)}
		mg.bridges[domain] = pool
	}
	mg.bridgesMu.Unlock()
	pool.lock.Lock()
	relay.registeredAt = time.Now()
	pool.availableConns = append(pool.availableConns, relay)
	if pool.relays == nil {
		pool.relays = map[*deviceRelay]struct{}{}
	}
	pool.relays[relay] = struct{}{}
	mg.onDeviceConnectionRegistered(domain, pool)
	relay.start()
	pool.lock.Unlock()
	return relay
}

// sessionsBridge is a test bridge whose device has n connections, each to
// a device that signs in the token "good" (newSignInDeviceServer). dialFrom
// opens a client socket from addr (the bridge trusts X-Forwarded-For from
// loopback, as from its own TLS front).
func sessionsBridge(t *testing.T, n int, delay func(int32) time.Duration) (mg *Manager, dialFrom func(addr string) *gorilla.Conn, host string) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i := 0; i < 200; i++ {
		mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
			WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))
	}
	mg = &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	_, host = newTestBridge(t, mg)
	t.Cleanup(func() { stopOfflineTimer(mg, host) })
	for i := 0; i < n; i++ {
		poolRelayTo(t, mg, host, newSignInDeviceServer(t, delay))
	}
	return mg, func(addr string) *gorilla.Conn {
		t.Helper()
		c, _, err := gorilla.DefaultDialer.Dial("ws://"+host, http.Header{"X-Forwarded-For": {addr}})
		if err != nil {
			t.Fatalf("dialing bridge: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}, host
}

func signInFrame(t *testing.T, id int32, token string) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.ReqEnvelope{Id: id, Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: token}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ask sends frame and reports whether the device answered it (false: the
// bridge said the device is unreachable).
func ask(t *testing.T, c *gorilla.Conn, frame []byte) bool {
	t.Helper()
	if err := c.WriteMessage(gorilla.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	resp := readResp(t, c)
	if ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck); ok && ack.RespAck.Code == cCodeDeviceUnreachable {
		return false
	}
	return true
}

// closedSoon reports whether the bridge hangs up on c within d.
func closedSoon(c *gorilla.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return !strings.Contains(err.Error(), "timeout")
		}
	}
}

func anonCount(mg *Manager) int {
	mg.pairedMu.Lock()
	defer mg.pairedMu.Unlock()
	n := 0
	for k, v := range mg.anonByKey {
		if !strings.Contains(k, "/48|") {
			n += v
		}
	}
	return n
}

// One address may hold only a share of a device - a quarter of its cap -
// in sessions that haven't signed in; the sessions that sign in don't
// count, and give their place back as they do.
func TestSessionsThatNeverSignInGetAShareOfTheDevice(t *testing.T) {
	setVar(t, &cDefaultMaxPairedPerDevice, 8) // a share of 2
	setVar(t, &cAnonShareWait, 100*time.Millisecond)
	_, dialFrom, _ := sessionsBridge(t, 8, nil)

	const stranger = "198.51.100.7"
	if !ask(t, dialFrom(stranger), envelopeFrame(t, 1)) || !ask(t, dialFrom(stranger), envelopeFrame(t, 1)) {
		t.Fatal("the first two sessions of an address were refused")
	}
	if ask(t, dialFrom(stranger), envelopeFrame(t, 1)) {
		t.Fatal("a third session that never signed in, from the same address, went through")
	}
	// An IPv6 address's /48 is one share however many /64s it has.
	if !ask(t, dialFrom("2001:db8:7:1::1"), envelopeFrame(t, 1)) || !ask(t, dialFrom("2001:db8:7:2::1"), envelopeFrame(t, 1)) {
		t.Fatal("the first two sessions of a /48 were refused")
	}
	if ask(t, dialFrom("2001:db8:7:3::1"), envelopeFrame(t, 1)) {
		t.Fatal("a third /64 of the same /48 got past its share")
	}
	// Another address has its own share; and a session that signs in
	// leaves its address's share at once.
	const owner = "203.0.113.9"
	first := dialFrom(owner)
	if !ask(t, first, envelopeFrame(t, 1)) || !ask(t, first, signInFrame(t, 2, "good")) {
		t.Fatal("the owner was refused")
	}
	if !ask(t, dialFrom(owner), signInFrame(t, 1, "good")) || !ask(t, dialFrom(owner), envelopeFrame(t, 1)) || !ask(t, dialFrom(owner), envelopeFrame(t, 1)) {
		t.Fatal("signed-in sessions took up their address's share of strangers")
	}
}

// A refused token is not a sign-in: the session stays a stranger's.
func TestARefusedSignInLeavesTheSessionAStrangers(t *testing.T) {
	setVar(t, &cDefaultMaxPairedPerDevice, 4) // a share of 1
	setVar(t, &cAnonShareWait, 100*time.Millisecond)
	_, dialFrom, _ := sessionsBridge(t, 4, nil)
	c := dialFrom("198.51.100.7")
	if !ask(t, c, signInFrame(t, 1, "wrong")) {
		t.Fatal("the first session was refused")
	}
	if ask(t, dialFrom("198.51.100.7"), envelopeFrame(t, 1)) {
		t.Fatal("a refused sign-in freed its address's share")
	}
}

// Every web tab and every app holds one paired socket for as long as it
// is open: a household behind one NAT - a browser restoring about 20
// tabs, phones, the desktop app, otc-sync - keeps all of them under the
// default limits, since each signs in (a tab's first request may be its
// key, the sign-in right after).
func TestAHouseholdBehindOneAddressKeepsItsSessions(t *testing.T) {
	const sessions = 24
	mg, dialFrom, _ := sessionsBridge(t, sessions, nil)
	mg.pairPerAddr = nil
	var tabs []*gorilla.Conn
	for i := 0; i < sessions; i++ {
		tabs = append(tabs, dialFrom("203.0.113.9"))
	}
	var wg sync.WaitGroup
	errs := make(chan string, sessions)
	for i, c := range tabs { // all at once, as a browser restores them
		wg.Add(1)
		go func(i int, c *gorilla.Conn) {
			defer wg.Done()
			if i%2 == 0 { // the key first, as the web app's sign-in page asks
				if err := c.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, 1)); err != nil {
					errs <- err.Error()
					return
				}
				if resp := readResp(t, c); resp.Payload != nil {
					errs <- fmt.Sprintf("tab %d was refused", i+1)
					return
				}
			}
			if err := c.WriteMessage(gorilla.BinaryMessage, signInFrame(t, 2, "good")); err != nil {
				errs <- err.Error()
				return
			}
			if resp := readResp(t, c); resp.GetRespAck() == nil || !resp.GetRespAck().Ok {
				errs <- fmt.Sprintf("tab %d could not sign in: %v", i+1, resp)
			}
		}(i, c)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	waitFor(t, func() bool { return anonCount(mg) == 0 }, "every signed-in tab out of the strangers' share")
}

// A session that never signed in and has said nothing for cAnonPairedIdle
// is closed, unless a request of it is still in flight; one that signed in
// may go quiet for as long as it likes.
func TestAQuietSessionThatNeverSignedInIsClosed(t *testing.T) {
	setVar(t, &cAnonPairedIdle, 300*time.Millisecond)
	slow := int32(7)
	mg, dialFrom, _ := sessionsBridge(t, 4, func(id int32) time.Duration {
		if id == slow {
			return time.Second // more than three idle periods
		}
		return 0
	})

	stranger := dialFrom("198.51.100.7")
	if !ask(t, stranger, envelopeFrame(t, 1)) {
		t.Fatal("refused")
	}
	if !closedSoon(stranger, 3*time.Second) {
		t.Fatal("a session that never signed in stayed open while quiet")
	}

	waiting := dialFrom("198.51.100.8")
	if !ask(t, waiting, envelopeFrame(t, 1)) {
		t.Fatal("refused")
	}
	if !ask(t, waiting, envelopeFrame(t, slow)) {
		t.Fatal("a request in flight longer than the idle period lost its session")
	}

	owner := dialFrom("203.0.113.9")
	if !ask(t, owner, signInFrame(t, 1, "good")) {
		t.Fatal("refused")
	}
	time.Sleep(3 * cAnonPairedIdle)
	if !ask(t, owner, envelopeFrame(t, 2)) {
		t.Fatal("a signed-in session was closed while quiet")
	}
	waitFor(t, func() bool { return anonCount(mg) == 0 }, "the closed sessions' shares given back")
}

// At the device's cap, a newcomer closes an idle session that never
// signed in - the one of the address holding the most of them - so
// strangers can't keep the device's own people out; signed-in sessions
// are never closed for one, and nor is a session with a request in flight.
func TestANewcomerMakesRoomByClosingAnIdleStranger(t *testing.T) {
	setVar(t, &cDefaultMaxPairedPerDevice, 4) // a share of 1
	setVar(t, &cEvictAfter, 50*time.Millisecond)
	setVar(t, &cAnonShareWait, 100*time.Millisecond)
	_, dialFrom, _ := sessionsBridge(t, 8, nil)

	var strangers []*gorilla.Conn
	for i := 1; i <= 3; i++ {
		c := dialFrom(fmt.Sprintf("198.51.100.%d", i))
		if !ask(t, c, envelopeFrame(t, 1)) {
			t.Fatalf("stranger %d was refused", i)
		}
		strangers = append(strangers, c)
	}
	member := dialFrom("203.0.113.10")
	if !ask(t, member, signInFrame(t, 1, "good")) {
		t.Fatal("refused")
	}
	time.Sleep(100 * time.Millisecond)

	// The device is at its cap: the owner gets in, and one stranger is out.
	owner := dialFrom("203.0.113.9")
	if !ask(t, owner, signInFrame(t, 1, "good")) {
		t.Fatal("the owner was kept out by sessions that never signed in")
	}
	out := 0
	for _, c := range strangers {
		if closedSoon(c, time.Second) {
			out++
		}
	}
	if out != 1 {
		t.Errorf("%d strangers were closed for the owner, want 1", out)
	}
	if !ask(t, member, envelopeFrame(t, 2)) {
		t.Fatal("a signed-in session was closed for a newcomer")
	}
}

// With every connection in use by signed-in sessions, a newcomer is told
// the device is unreachable, as before: nobody is closed for it.
func TestSignedInSessionsAreNeverClosedForANewcomer(t *testing.T) {
	setVar(t, &cDefaultMaxPairedPerDevice, 2)
	setVar(t, &cEvictAfter, 10*time.Millisecond)
	_, dialFrom, _ := sessionsBridge(t, 4, nil)
	a, b := dialFrom("203.0.113.9"), dialFrom("203.0.113.10")
	if !ask(t, a, signInFrame(t, 1, "good")) || !ask(t, b, signInFrame(t, 1, "good")) {
		t.Fatal("refused")
	}
	time.Sleep(50 * time.Millisecond)
	if ask(t, dialFrom("198.51.100.7"), envelopeFrame(t, 1)) {
		t.Fatal("a newcomer went past the device's cap")
	}
	if !ask(t, a, envelopeFrame(t, 2)) || !ask(t, b, envelopeFrame(t, 2)) {
		t.Fatal("a signed-in session was closed")
	}
}

// A client whose device connection died while it was idle is hung up on
// at once: its next request then opens a socket with a live connection
// instead of being told the device is unreachable.
func TestAnIdleClientIsHungUpOnWhenItsDeviceConnectionDies(t *testing.T) {
	mg, dialFrom, host := sessionsBridge(t, 1, nil)
	c := dialFrom("203.0.113.9")
	if !ask(t, c, signInFrame(t, 1, "good")) {
		t.Fatal("refused")
	}
	mg.dropPool(host) // the device's connection goes (a restart, a network change)
	if !closedSoon(c, 2*time.Second) {
		t.Fatal("the client stayed paired with a dead connection")
	}
}

// Sign-ins are read off the wire: the payload is the envelope's last field
// but its id and language, as the device decodes it, and only an Ack with
// ok and no error is an accepted one.
func TestSignInsAreReadOffTheWire(t *testing.T) {
	m := func(env *pb.ReqEnvelope) []byte {
		b, err := proto.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	auth := m(&pb.ReqEnvelope{Id: 3, Lang: "es", Payload: &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Key: []byte("k")}}})
	token := m(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: "t"}}})
	friend := m(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqAuthAsFriend{ReqAuthAsFriend: &pb.AuthAsFriend{Domain: "x"}}})
	pubKey := m(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}})
	for name, c := range map[string]struct {
		frame []byte
		want  bool
	}{
		"auth":                 {auth, true},
		"token":                {token, true},
		"friend":               {friend, true},
		"another request":      {pubKey, false},
		"auth then another":    {append(append([]byte{}, auth...), pubKey[2:]...), false},
		"another then auth":    {append(append([]byte{}, pubKey...), auth[2:]...), true},
		"auth then a lang":     {protowire.AppendString(protowire.AppendTag(append([]byte{}, auth...), 2, protowire.BytesType), "fr"), true},
		"auth then an unknown": {protowire.AppendVarint(protowire.AppendTag(append([]byte{}, auth...), 9999, protowire.VarintType), 1), false},
		"nothing":              {nil, false},
		"broken":               {[]byte{0xff, 0xff}, false},
		"auth with a bad tail": {append(append([]byte{}, auth...), 0x62, 0x05), false},
	} {
		if got := isSignIn(c.frame); got != c.want {
			t.Errorf("%s: isSignIn = %v, want %v", name, got, c.want)
		}
	}

	r := func(env *pb.RespEnvelope) []byte {
		b, err := proto.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ok := r(&pb.RespEnvelope{Id: 3, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}})
	for name, c := range map[string]struct {
		resp []byte
		want bool
	}{
		"ok":                 {ok, true},
		"refused":            {r(&pb.RespEnvelope{Id: 3, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: false, ErrorMsg: "no"}}}), false},
		"ok but an error":    {r(&pb.RespEnvelope{Id: 3, Error: true, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}), false},
		"unreachable":        {mustUnreachable(t, 3), false},
		"another payload":    {r(&pb.RespEnvelope{Id: 3, Payload: &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{}}}), false},
		"ok then another":    {append(append([]byte{}, ok...), r(&pb.RespEnvelope{Payload: &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{}}})...), false},
		"just an id":         {r(&pb.RespEnvelope{Id: 3}), false},
		"error text, no ack": {r(&pb.RespEnvelope{Id: 3, ErrorMessage: "x"}), false},
		"broken":             {[]byte{0x5a}, false},
	} {
		if got := isAccepted(c.resp); got != c.want {
			t.Errorf("%s: isAccepted = %v, want %v", name, got, c.want)
		}
	}
}

func mustUnreachable(t *testing.T, id int32) []byte {
	t.Helper()
	b, err := deviceUnreachableReply(id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An id sent again while the first was in flight can't say whose answer
// came back: neither is read as a sign-in.
func TestADuplicatedIDIsNeverASignIn(t *testing.T) {
	c, _, err := gorilla.DefaultDialer.Dial(newSignInDeviceServer(t, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := newClientSession("pit.otc", []string{"k"}, c, nil)
	s.started(5)
	s.started(5)
	if s.finished(5, true) {
		t.Fatal("an answer to a duplicated id signed the session in")
	}
	if s.finished(5, true) {
		t.Fatal("the other answer to a duplicated id signed the session in")
	}
	s.started(5)
	if !s.finished(5, true) {
		t.Fatal("an id sent once at a time is a sign-in again")
	}
}
