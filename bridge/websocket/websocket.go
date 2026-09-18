// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

const (
	CEndpoint = "/ws"

	// Issue #53 follow-up: a device's own pool grows dynamically under
	// load now instead of dialing a fixed count once (see
	// websocket.ensureBridgePool on the device side) - this default caps
	// that growth if [bridge] is left unconfigured, so one device can't
	// accumulate an unbounded number of idle connections here.
	cDefaultMaxConnectionsPerDevice = 100

	// cDeviceUnreachableMsg is what a client is told when a domain is
	// registered here but its device currently has no connection to relay
	// through (issue #56). Written for whoever is actually reading it -
	// the old text was "No available connections in the pool for this
	// device", which describes the bridge's internal bookkeeping rather
	// than the only thing the reader can act on: the device is off or
	// offline, and this is worth retrying. Clients key their "not
	// reachable" UI off Ack.code (cCodeDeviceUnreachable below) rather
	// than this prose, so the wording is free to change without breaking
	// them.
	cDeviceUnreachableMsg = "This device isn't reachable right now. It may be switched off or offline - please try again later."

	// Ack.code values (issue #56) - the stable tags a client reacts to,
	// as opposed to the human-readable text alongside them. See the
	// `code` field's own comment in proto/messages.proto.
	cCodeDeviceUnreachable = "device_unreachable"
	cCodeAccountDisabled   = "account_disabled"
)

// cOfflineAlertGrace is how long a device can have zero live bridge
// connections before it's treated as genuinely offline and a push alert
// goes out (issue #62). A device's own pool refills continuously while
// it's online - see websocket.ensureBridgePool's doc comment on the device
// side: 5 ready, refilling in batches of 2 once it dips to 3 - so briefly
// touching zero under simultaneous client load and refilling well within
// this window is expected and must never alert. Var, not const, so tests
// can shrink it instead of waiting on a real minute.
var cOfflineAlertGrace = 60 * time.Second

// cForwardTimeout bounds how long deviceRelay.forward waits for a
// response - see its own doc comment for the real bug this backstops
// (a relay that died via a ping/pong timeout, not an explicit Close, could
// leave a later forward() call waiting on a response nothing would ever
// deliver). Generous rather than tight: a real GetFile for a large photo/
// video over a slow connection can legitimately take a while, and this
// only exists to catch "will actually never answer", not to race normal
// slow requests. Var, not const, so tests can shrink it.
var cForwardTimeout = 90 * time.Second

// maxConnectionsPerDevice reads [bridge] max-connections-per-device,
// falling back to cDefaultMaxConnectionsPerDevice if that section/key is
// absent - deliberately optional config, not a required one, so existing
// deployments don't need an ini change just to pick up this cap.
func maxConnectionsPerDevice() int {
	if !cfg.HasSection("bridge") {
		return cDefaultMaxConnectionsPerDevice
	}
	if v := cfg.GetInt("bridge", "max-connections-per-device"); v > 0 {
		return int(v)
	}
	return cDefaultMaxConnectionsPerDevice
}

// bridgePool is one device's spare connections plus its offline-detection
// state (issue #62). liveCount is every connection currently held for this
// domain, idle-in-availableConns or already claimed for one client's relay
// - not just the idle ones - since a connection being picked and single-
// used doesn't mean the device went away, only that this particular tunnel
// is spent; see the doc comment on deviceRelay.onDeath for how this and
// availableConns are kept in sync.
type bridgePool struct {
	availableConns []*deviceRelay
	liveCount      int
	// offlineTimer is non-nil exactly while a countdown is pending -
	// started the instant liveCount drops to zero, stopped/cleared the
	// instant a fresh registration brings it back above zero. If it fires
	// with liveCount still at zero, the device is treated as offline.
	offlineTimer *time.Timer
	lock         *sync.Mutex
}

// deviceRelay wraps one paired device connection with request/response
// multiplexing by envelope id, so several client requests can be in flight
// to the same device connection at once instead of strictly one at a time.
// The device itself now processes a connection's requests concurrently
// (see websocket.handleConnection's doc comment on the device side, added
// for the same reason: a slow request — a large GetFile needing a HEIC
// decode, say — used to sit in front of a cheap, unrelated one like
// GetFileInfo, on the very same connection). Relaying them here strictly
// one at a time would reintroduce that identical head-of-line blocking one
// hop earlier, this time in a place the device-side fix can't reach at
// all — every request/response for a given client<->device pairing was
// forced through a single write-then-block-for-the-matching-read step
// before the bridge would even read the client's next frame.
// cPongWait/cPingPeriod (issue #62) are what actually let a truly-dead
// device connection be noticed. A connection idle in the pool never has
// anything written to it until picked, so a graceful shutdown (the device
// process exiting, which sends a real TCP close) and a genuine network
// failure (a cut cable, killed wifi, a powered-off Pi - no FIN, no RST,
// just silence) are NOT the same failure from the bridge's point of view:
// the former makes ReadMessage() return an error immediately, but the
// latter leaves it blocked forever with nothing to time it out. Every
// relay - idle or already claimed - gets an active ping/pong keepalive for
// its whole life so both cases end up looking the same: a ReadMessage()
// error, routed through failAll() into onDeath the same way either way.
// cPongWait bounds how long a connection can go without word from the
// device (a pong, or - moot in practice since pongs come far more often,
// but harmless either way - genuine traffic) before it's declared dead;
// cPingPeriod keeps probing well inside that window so a healthy-but-idle
// connection never times out on its own inactivity. Vars, not consts, so
// tests can shrink them instead of waiting on real tens-of-seconds delays.
var (
	cPongWait   = 40 * time.Second
	cPingPeriod = (cPongWait * 8) / 10
)

type deviceRelay struct {
	conn    *gorilla.Conn
	writeMu sync.Mutex // gorilla tolerates only one concurrent writer

	mu      sync.Mutex
	waiters map[int32]chan []byte

	// onDeath (issue #62) fires exactly once, from failAll, whenever this
	// connection stops being usable - whether that's a real network
	// failure, a ping/pong timeout (see cPongWait above), or just Close()
	// being called because a single-use relay finished its one job.
	// Either way this device connection is gone from the pool now, which
	// is exactly the signal bridgePool.liveCount needs; it doesn't matter
	// to the offline-detection logic *why* a given connection died, only
	// that the device keeps a healthy count of live ones by continuously
	// registering new ones while it's actually online.
	onDeath func()
	once    sync.Once

	stopPing chan struct{}  // closed once, from failAll, to stop pingLoop
	wg       sync.WaitGroup // readLoop + pingLoop, so Close() can wait for both to actually exit
}

// newDeviceRelay wraps conn and immediately starts reading from it via
// readLoop - including for a connection that's about to sit idle in a
// pool's availableConns rather than being handed to a client right away.
// That immediacy matters for issue #62: previously a relay (and its read
// loop) was only created lazily once a connection got picked, so a device
// connection that died while still idle in the pool went completely
// unnoticed until something eventually tried to use it. Reading from the
// moment a device registers is what lets a dead idle connection be
// detected (and liveCount decremented) right away instead - and pairing
// that with the ping/pong keepalive below (also started here, for the same
// reason) is what makes that detection actually fire for a silent network
// failure, not just a graceful shutdown.
func newDeviceRelay(conn *gorilla.Conn, onDeath func()) *deviceRelay {
	d := &deviceRelay{conn: conn, waiters: make(map[int32]chan []byte), onDeath: onDeath, stopPing: make(chan struct{})}

	conn.SetReadDeadline(time.Now().Add(cPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(cPongWait))
		return nil
	})

	d.wg.Add(2)
	go func() { defer d.wg.Done(); d.readLoop() }()
	go func() { defer d.wg.Done(); d.pingLoop() }()
	return d
}

// pingLoop is deviceRelay's half of the keepalive - see cPongWait/
// cPingPeriod's doc comment. Runs for the relay's whole life, whether it's
// sitting idle in the pool or already claimed for one client's relay;
// stops the instant failAll runs, same as readLoop.
func (d *deviceRelay) pingLoop() {
	ticker := time.NewTicker(cPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.writeMu.Lock()
			err := d.conn.WriteMessage(gorilla.PingMessage, nil)
			d.writeMu.Unlock()
			if err != nil {
				// readLoop's own ReadMessage() will fail from the same
				// dead connection and drive failAll/onDeath - nothing
				// more to do here than stop pinging a socket that's
				// already gone.
				return
			}
		case <-d.stopPing:
			return
		}
	}
}

// readLoop is this relay's one and only reader — gorilla tolerates only
// one concurrent reader, same as one writer — so every response coming
// back from the device passes through here and gets routed to whichever
// forward() call is waiting on that response's envelope id.
func (d *deviceRelay) readLoop() {
	for {
		_, frame, err := d.conn.ReadMessage()
		if err != nil {
			d.failAll()
			return
		}
		var env pb.RespEnvelope
		if err := proto.Unmarshal(frame, &env); err != nil {
			log.Error("bad proto from device:", err)
			continue
		}
		d.mu.Lock()
		ch, ok := d.waiters[env.Id]
		if ok {
			delete(d.waiters, env.Id)
		}
		d.mu.Unlock()
		if ok {
			ch <- frame
		}
		// No waiter for this id (already gave up, or a stray/duplicate
		// message) — nothing to deliver it to, so just drop it.
	}
}

// failAll unblocks every still-pending forward() call once the device
// connection itself has died, instead of leaving each one hanging forever
// waiting on a response that will now never arrive.
func (d *deviceRelay) failAll() {
	d.mu.Lock()
	waiters := d.waiters
	d.waiters = make(map[int32]chan []byte)
	d.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}

	// Close the underlying socket here, not just when the public Close()
	// is called explicitly - readLoop's error path (a ping/pong read-
	// deadline timeout in particular) means *our own* read gave up, but
	// says nothing about the OS-level TCP connection, which a bare
	// deadline expiry never touches. Without this, a relay that died this
	// way stayed sitting in the pool as a connection that still *looks*
	// writable (WriteMessage on a merely-deadline-expired socket can
	// still succeed, buffered locally) - if it was later picked and
	// forward() called again, it registered a waiter that nothing would
	// ever fulfill (this relay's one and only readLoop had already
	// returned for good), hanging that request forever with no timeout of
	// its own. Closing here guarantees any later write on this relay
	// fails fast instead. (Calling the underlying conn.Close() directly,
	// not the public Close() method - that one calls wg.Wait(), which
	// would deadlock: failAll only ever runs from inside readLoop itself,
	// before its own wg.Done() has fired.)
	d.conn.Close()

	// Stop pingLoop - readLoop (the only caller of failAll) is the one
	// exiting right after this, so there's nothing left probing this
	// connection's liveness once it's gone anyway.
	close(d.stopPing)

	// Exactly once per relay, regardless of how many times failAll could
	// theoretically run (readLoop only calls it the one time it returns,
	// but once.Do costs nothing and removes any doubt) - see onDeath's
	// doc comment for what this drives.
	if d.onDeath != nil {
		d.once.Do(d.onDeath)
	}
}

// forward sends one request frame to the device and returns its matching
// response frame, correlated by envelope id — safe to call concurrently
// for several requests in flight on the same relay at once.
func (d *deviceRelay) forward(frame []byte) ([]byte, error) {
	return d.forwardWithTimeout(frame, cForwardTimeout)
}

// forwardWithTimeout is forward with an explicit timeout - factored out
// for ForwardOneOff (issue #95), whose candidates are popped off a pool
// that a device's own past process restarts can leave holding connections
// nothing will ever answer on again (see ForwardOneOff's own doc comment).
// cForwardTimeout's 90s is tuned for a real GetFile of a large photo/video
// over a slow connection; waiting anywhere near that long per bad
// candidate before trying the next one turned a handful of stale pool
// entries into a multi-minute stall for what should be a near-instant
// static asset fetch - reproduced live the first time this path actually
// ran against a device whose pool had accumulated any (issue #95's own
// testing, against a device redeployed and restarted many times over one
// long session).
func (d *deviceRelay) forwardWithTimeout(frame []byte, timeout time.Duration) ([]byte, error) {
	var env pb.ReqEnvelope
	if err := proto.Unmarshal(frame, &env); err != nil {
		return nil, fmt.Errorf("bad proto: %w", err)
	}

	ch := make(chan []byte, 1)
	d.mu.Lock()
	d.waiters[env.Id] = ch
	d.mu.Unlock()

	d.writeMu.Lock()
	err := d.conn.WriteMessage(gorilla.BinaryMessage, frame)
	d.writeMu.Unlock()
	if err != nil {
		d.mu.Lock()
		delete(d.waiters, env.Id)
		d.mu.Unlock()
		return nil, err
	}

	// A bounded wait, not just <-ch: the failAll() fix above (closing the
	// underlying socket on any death, not just an explicit Close()) should
	// mean a relay can no longer end up in a state where nothing will ever
	// answer this waiter, but this is the backstop for that guarantee
	// being wrong in some case not yet found - the actual, live bug this
	// closes was a client's request (and the client itself) hanging
	// forever with no error at all, which is worse than the request just
	// failing.
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("device connection closed")
		}
		return resp, nil
	case <-time.After(timeout):
		d.mu.Lock()
		delete(d.waiters, env.Id)
		d.mu.Unlock()
		return nil, fmt.Errorf("timed out waiting for device response")
	}
}

// Close closes the underlying connection and waits for readLoop/pingLoop to
// both actually exit before returning - not just kicking them off. Beyond
// making shutdown deterministic in general, this is what keeps cPongWait/
// cPingPeriod safe for tests to override: without it, a relay's background
// goroutines could still be running (briefly) after Close() returns, racing
// a later test's change to those package vars.
func (d *deviceRelay) Close() error {
	err := d.conn.Close()
	d.wg.Wait()
	return err
}

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl   string
	dao       *dao.Dao
	upgrader  gorilla.Upgrader
	bridges   map[string]*bridgePool // The domain is the key and the value the pool of connections
	bridgesMu sync.RWMutex           // guards the bridges map itself, not each pool's own contents (pool.lock does that)
}

func Init(baseUrl string, dao *dao.Dao) (mg *Manager) {
	mg = &Manager{
		baseUrl: baseUrl,
		dao:     dao,
		upgrader: gorilla.Upgrader{
			// In production, set a proper origin check!
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		bridges: make(map[string]*bridgePool),
	}

	return
}

// domainPushStorage adapts *dao.Dao's per-domain push-registration methods
// to push.Storage (issue #62) - scoped to one domain, since the bridge (
// unlike a device's own dao.Dao, which only ever holds that one device's
// registrations) holds every domain's, so each read/write needs to say
// which one.
type domainPushStorage struct {
	dao    *dao.Dao
	domain string
}

func (s *domainPushStorage) GetVapidKeys() (pub, priv string, err error) {
	return s.dao.GetVapidKeysForDomain(s.domain)
}
func (s *domainPushStorage) SetVapidKeys(pub, priv string) error {
	return s.dao.SetVapidKeysForDomain(s.domain, pub, priv)
}
func (s *domainPushStorage) ListWebPushSubscriptions() ([]*push.WebPushSubscription, error) {
	return s.dao.ListWebPushSubscriptionsForDomain(s.domain)
}
func (s *domainPushStorage) DeleteWebPushSubscription(endpoint string) error {
	return s.dao.DeleteWebPushSubscriptionForDomain(s.domain, endpoint)
}
func (s *domainPushStorage) ListApnsTokens() ([]string, error) {
	return s.dao.ListApnsTokensForDomain(s.domain)
}
func (s *domainPushStorage) DeleteApnsToken(t string) error {
	return s.dao.DeleteApnsTokenForDomain(s.domain, t)
}

// sendOfflineAlert (issue #62) is called once cOfflineAlertGrace has
// elapsed with domain's liveCount still at zero - see
// onDeviceConnectionRegistered/onDeviceConnectionDied below for how that's
// tracked. Builds a push.Push backed by this one domain's mirrored
// registrations and sends a single generic notification, same as any other
// push.Push.Notify call - no post content here, just "you might want to
// check on this".
func (mg *Manager) sendOfflineAlert(domain string) {
	log.Info("device has had no live bridge connections for", cOfflineAlertGrace, "- alerting owner:", domain)
	ps, err := push.Init(&domainPushStorage{dao: mg.dao, domain: domain})
	if err != nil {
		log.Error("could not init push for offline alert:", domain, err)
		return
	}
	ps.Notify("Off The Cloud", "Your device appears to have gone offline")
}

// onDeviceConnectionRegistered records that domain just gained one more
// live bridge connection (a fresh ReqBridgeRegister) - cancelling any
// pending offline countdown, since the device is provably reachable again.
// Must be called with pool.lock held.
func (mg *Manager) onDeviceConnectionRegistered(domain string, pool *bridgePool) {
	pool.liveCount++
	if pool.offlineTimer != nil {
		pool.offlineTimer.Stop()
		pool.offlineTimer = nil
		log.Info("device reconnected before its offline alert fired, cancelling countdown:", domain)
	}
}

// onDeviceConnectionDied is deviceRelay.onDeath for every relay in domain's
// pool (idle or already claimed - see bridgePool's doc comment for why
// both count). When this decrement is what takes liveCount to zero, it
// starts the cOfflineAlertGrace countdown per the exact design: "if the
// device connections goes from > 1 to 0 then start a go routine with a
// count down that will send the notification if it takes more than 1 min
// to go back to 1".
func (mg *Manager) onDeviceConnectionDied(domain string) {
	mg.bridgesMu.RLock()
	pool, ok := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if !ok {
		return
	}

	pool.lock.Lock()
	defer pool.lock.Unlock()

	if pool.liveCount > 0 {
		pool.liveCount--
	}
	if pool.liveCount == 0 && pool.offlineTimer == nil {
		log.Info("device has zero live bridge connections, starting offline countdown:", domain)
		pool.offlineTimer = time.AfterFunc(cOfflineAlertGrace, func() { mg.fireOfflineAlertIfStillDown(domain) })
	}
}

// fireOfflineAlertIfStillDown is cOfflineAlertGrace's timer callback -
// re-checks liveCount under the pool's own lock (rather than trusting the
// state at the moment the timer was scheduled) to close the narrow race
// against a registration landing right as this fires.
func (mg *Manager) fireOfflineAlertIfStillDown(domain string) {
	mg.bridgesMu.RLock()
	pool, ok := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if !ok {
		return
	}

	pool.lock.Lock()
	stillDown := pool.liveCount == 0
	pool.offlineTimer = nil
	pool.lock.Unlock()

	if stillDown {
		mg.sendOfflineAlert(domain)
	}
}

func (mg *Manager) closeWithError(conn *gorilla.Conn, id int32, err error) {
	log.Error("closing socket with error:", err)
	// Acknoledge the authentication
	respAuth := &pb.RespEnvelope{
		Id: id,
		Payload: &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{
				Ok:       false,
				ErrorMsg: fmt.Sprintf("Error: %s", err),
			},
		},
	}
	resp, _ := proto.Marshal(respAuth)
	if err := conn.WriteMessage(gorilla.BinaryMessage, resp); err != nil {
		log.Error("error responding, closing the connection:", err)
	}
	conn.Close()

}

// cOneOffForwardTimeout bounds each candidate connection's forward() call
// in ForwardOneOff - shorter than cForwardTimeout (90s, tuned for a real
// GetFile of a large photo/video), because a candidate that never answers
// is usually a stale pool entry and 90s of that per candidate is what
// turned a handful of them into a multi-minute stall, reproduced live.
//
// But "static assets are small and answer almost immediately" - the
// reasoning behind the original 5s - was simply wrong, and 5s turned out
// to be actively harmful: the web app's own JS bundle is ~800KB and takes
// around 3s through the relay on a *good* run, so any slower moment blew
// the deadline. Each expiry then burned another pool connection (this
// closes every candidate it gives up on) and moved to the next, so one
// slow bundle fetch could empty a 20-connection pool in seconds and leave
// the whole device answering "no available connections" - to every
// request, not just the big one - until the device refilled. Caught live:
// a page load that fetched its CSS fine and timed out on its JS, then
// took the site down for everything.
//
// 45s instead: still far short of the 90s stall this exists to prevent,
// but comfortably above what a genuinely working transfer needs over a
// home uplink. Var, not const, so tests can shrink it.
var cOneOffForwardTimeout = 45 * time.Second

// cOneOffMaxAttempts caps how many pool connections one request may spend
// before giving up. Without a cap, ForwardOneOff walks the *entire* pool
// on any repeated failure - closing each connection as it goes - so a
// single bad request could take a healthy device offline for everyone
// until it refilled. Three is enough to step past a couple of genuinely
// stale entries without ever being able to drain the pool.
const cOneOffMaxAttempts = 3

// ForwardOneOff sends frame to one connection from domain's pool and
// returns the raw response frame, always closing that connection
// afterward - unlike Listen's own default pass-through case below, which
// keeps a picked connection paired for the rest of a browser's WS session,
// this has no session of its own to keep it for: issue #95's static-asset
// proxy is a plain HTTP handler with a fresh, independent request every
// time, so each call here gets its own connection and gives it straight
// back. Same pool-pick loop as that default case (skip a candidate that
// fails to forward, try the next one) - see deviceRelay.forward's own doc
// comment for why a single bad connection shouldn't fail the whole
// request when another one might still work - except each candidate here
// gets cOneOffForwardTimeout, not cForwardTimeout: popping candidates one
// at a time and only holding pool.lock long enough to pop each one (not
// for the whole loop, unlike the default case) means a slow/stale
// candidate here blocks this one caller, not every other request trying
// to reach the same device concurrently.
func (mg *Manager) ForwardOneOff(domain string, frame []byte) (respFrame []byte, err error) {
	mg.bridgesMu.RLock()
	pool, ok := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if !ok {
		return nil, errors.New("device is offline")
	}

	for attempt := 0; attempt < cOneOffMaxAttempts; attempt++ {
		pool.lock.Lock()
		if len(pool.availableConns) == 0 {
			pool.lock.Unlock()
			return nil, errors.New("no available connections in the pool for this device")
		}
		candidate := pool.availableConns[0]
		pool.availableConns = pool.availableConns[1:]
		pool.lock.Unlock()

		respFrame, err = candidate.forwardWithTimeout(frame, cOneOffForwardTimeout)
		candidate.Close()
		if err != nil {
			log.Error("error forwarding one-off request, trying the next available connection:", err)
			continue
		}
		if dbErr := mg.dao.RecordDeviceActivity(domain, int64(len(frame)), int64(len(respFrame))); dbErr != nil {
			log.Error("error recording device activity:", dbErr)
		}
		return respFrame, nil
	}
	return nil, fmt.Errorf("device did not answer after %d attempts", cOneOffMaxAttempts)
}

func (mg *Manager) Listen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	conn, err := mg.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("error trying to upgrade the websocket:", err)
		return
	}

	//defer conn.Close()

	mg.handleConnection(conn, r)
}

// deviceUnreachableFrame builds the "this device isn't reachable" reply
// for a client request the bridge couldn't deliver (issue #56), echoing
// that request's own envelope id so the caller waiting on it actually
// settles - a response with the wrong id would be just as good as no
// response at all to a client that correlates by id (see WSClient's
// waiters map on the web side, and OTCConnection's on iOS).
func deviceUnreachableFrame(reqFrame []byte) ([]byte, error) {
	var req pb.ReqEnvelope
	if err := proto.Unmarshal(reqFrame, &req); err != nil {
		return nil, err
	}
	return proto.Marshal(&pb.RespEnvelope{
		Id:           req.Id,
		Error:        true,
		ErrorMessage: cDeviceUnreachableMsg,
		Payload: &pb.RespEnvelope_RespAck{
			RespAck: &pb.Ack{Ok: false, ErrorMsg: cDeviceUnreachableMsg, Code: cCodeDeviceUnreachable},
		},
	})
}

func (mg *Manager) handleConnection(conn *gorilla.Conn, r *http.Request) {
	// This serves every device and client connection the bridge relays, so
	// a bug triggered by any single one of them (a malformed message, an
	// edge case in a handler below) must not be able to take the whole
	// bridge — and every device relying on it — down with an unrecovered
	// panic. Closing just this connection is the correct blast radius.
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered from panic handling connection:", r, string(debug.Stack()))
			conn.Close()
		}
	}()

	// Once a client is paired with a device (relay != nil), each of its
	// requests is relayed in its own goroutine via relay.forward, which
	// multiplexes them over the one device connection by envelope id
	// instead of forcing them through one at a time — see deviceRelay's
	// doc comment for why that matters. writeMu serializes the responses
	// those goroutines write back to conn (the client side; gorilla
	// tolerates only one concurrent writer there too), and wg — waited on
	// before this function returns — makes sure none of them are left
	// trying to write to conn after it's already closed.
	var relay *deviceRelay
	var writeMu sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		_, frame, err := conn.ReadMessage()
		if err != nil {
			log.Error("error processing message:", err)
			return
		}

		if relay != nil {
			wg.Add(1)
			go func(frame []byte) {
				defer wg.Done()
				// Mirrors handleConnection's own top-level recover: this
				// now runs on its own goroutine, which the outer recover
				// above can't reach — an unrecovered panic here would
				// otherwise still take the whole process down.
				defer func() {
					if r := recover(); r != nil {
						log.Error("recovered from panic relaying message:", r, string(debug.Stack()))
					}
				}()

				respFrame, err := relay.forward(frame)
				if err != nil {
					// Issue #56: this is the mid-session half of "the
					// device isn't reachable" - the client was already
					// paired and working when the device went away (it was
					// switched off, lost its network, or is restarting for
					// a deploy). Logging and returning, as this used to,
					// left the client waiting on a reply that was never
					// coming: its request promise simply never settled, so
					// the app sat there looking like it was still loading,
					// indefinitely and silently. Answering with the same
					// RespAck a fresh connection gets means both routes
					// into this situation end up telling the client the
					// same thing.
					log.Error("error forwarding message, device unreachable:", err)
					unreachableFrame, mErr := deviceUnreachableFrame(frame)
					if mErr != nil {
						log.Error("error building unreachable response:", mErr)
						return
					}
					writeMu.Lock()
					wErr := conn.WriteMessage(gorilla.BinaryMessage, unreachableFrame)
					writeMu.Unlock()
					if wErr != nil {
						log.Error("error sending unreachable response:", wErr)
					}
					// Then hang up, because this client is pinned to this
					// one dead relay for as long as its connection lives
					// (see the relay != nil branch this sits in - a relay
					// is picked once, at first request, and never
					// re-picked). Leaving the connection open would mean
					// every subsequent request on it failing the same way
					// forever, even long after the device came back: the
					// client would sit on "not reachable" until someone
					// reloaded it by hand. Caught exactly that way in
					// testing - the device returned and the page kept
					// insisting it hadn't. Closing hands the client over
					// to its own reconnect (web: request() reconnects when
					// the socket isn't open; iOS: handleDisconnect's
					// backoff), and the fresh connection picks a live
					// relay - or gets the pool-empty answer above while
					// the device is still away.
					conn.Close()
					return
				}

				writeMu.Lock()
				writeErr := conn.WriteMessage(gorilla.BinaryMessage, respFrame)
				writeMu.Unlock()
				if writeErr != nil {
					log.Error("error forwading respose, closing the connection:", writeErr)
					return
				}

				if err := mg.dao.RecordDeviceActivity(r.Host, int64(len(frame)), int64(len(respFrame))); err != nil {
					// Metrics are best-effort: never fail the actual relay
					// over a metrics-write error.
					log.Error("error recording device activity:", err)
				}
			}(frame)
		} else {
			var env pb.ReqEnvelope
			if err := proto.Unmarshal(frame, &env); err != nil {
				log.Error("bad proto:", err)
				return
			}

			resp := &pb.RespEnvelope{
				Id: env.Id,
			}
			switch p := env.Payload.(type) {
			case *pb.ReqEnvelope_ReqBridgeRegister:
				log.Info("Register device")
				domain := p.ReqBridgeRegister.Domain
				// Check if we have the device already registered and if the pass is ok
				defined, validSecret, err := mg.dao.IsValidDevice(p.ReqBridgeRegister.OwnerUuid, domain, p.ReqBridgeRegister.Secret)
				if !defined {
					err = mg.dao.RegistreDevice(p.ReqBridgeRegister.OwnerUuid, domain, p.ReqBridgeRegister.Secret)
				}

				mg.bridgesMu.RLock()
				pool, ok := mg.bridges[domain]
				mg.bridgesMu.RUnlock()

				if defined && !validSecret {
					log.Error("error registering bridge:", err)
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
					// Someone tried to register an already-claimed domain with
					// the wrong owner_uuid/secret - could be a misconfigured
					// device, or someone probing for a weak/leaked secret.
					// Surfaced in the admin panel's security log (issue #8).
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), domain, p.ReqBridgeRegister.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
				} else if err != nil {
					log.Error("error trying to register:", err)
					resp.Error = true
					resp.ErrorMessage = err.Error()
				} else if ok && len(pool.availableConns) >= maxConnectionsPerDevice() {
					// Issue #53 follow-up: a device now grows its own pool
					// dynamically under load (see websocket.ensureBridgePool
					// on the device side) rather than dialing a fixed count
					// once - this is the backstop against that (or anything
					// else) growing one device's pool unbounded.
					log.Error("device at its connection cap, rejecting:", domain, len(pool.availableConns))
					resp.Error = true
					resp.ErrorMessage = "Device connection pool is full"
				} else {
					// The relay (and its read loop) is created right here,
					// at registration time, rather than lazily once picked -
					// see newDeviceRelay's doc comment for why that matters
					// to issue #62's offline detection.
					relay := newDeviceRelay(conn, func() { mg.onDeviceConnectionDied(domain) })

					// Re-check under the write lock (rather than trusting
					// the ok/pool snapshot read above) so two connections
					// registering the same brand-new domain at once can't
					// each create their own separate pool for it.
					mg.bridgesMu.Lock()
					pool, ok = mg.bridges[domain]
					if !ok {
						log.Debug("Creating new pool")
						pool = &bridgePool{
							availableConns: []*deviceRelay{relay},
							lock:           new(sync.Mutex),
						}
						mg.bridges[domain] = pool
						mg.bridgesMu.Unlock()
						pool.lock.Lock()
						mg.onDeviceConnectionRegistered(domain, pool)
						pool.lock.Unlock()
					} else {
						mg.bridgesMu.Unlock()
						log.Debug("Adding to the pool:", len(pool.availableConns))
						pool.lock.Lock()
						pool.availableConns = append(pool.availableConns, relay)
						mg.onDeviceConnectionRegistered(domain, pool)
						pool.lock.Unlock()
					}
					resp.Payload = &pb.RespEnvelope_RespBridgeAckOnboard{
						RespBridgeAckOnboard: &pb.BridgeAckOnboard{
							Ok: true,
						},
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
					log.Error("error responding, closing the connection:", err)
					conn.Close()
				}
				// After the connection is created, we leave it open and return
				return

			case *pb.ReqEnvelope_ReqRotateBridgeSecret:
				// Self-service "Regenerate" (issue #40 follow-up): the
				// device's current secret is the only thing that
				// authenticates this — see dao.RotateSecret's compare-and-
				// swap. A one-off request/response, not a pooled relay
				// connection, so this always closes the connection instead
				// of returning early like ReqBridgeRegister does above.
				defer conn.Close()
				log.Info("Rotate bridge secret for device:", p.ReqRotateBridgeSecret.Domain)
				newSecret := uuid.New().String() + uuid.New().String()
				ok, err := mg.dao.RotateSecret(
					p.ReqRotateBridgeSecret.OwnerUuid,
					p.ReqRotateBridgeSecret.Domain,
					p.ReqRotateBridgeSecret.Secret,
					newSecret,
				)
				if err != nil {
					log.Error("error rotating secret:", err)
					resp.Error = true
					resp.ErrorMessage = err.Error()
				} else if !ok {
					log.Error("rotate secret rejected: no matching device/secret")
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), p.ReqRotateBridgeSecret.Domain, p.ReqRotateBridgeSecret.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
				} else {
					resp.Payload = &pb.RespEnvelope_RespRotateBridgeSecretAck{
						RespRotateBridgeSecretAck: &pb.RotateBridgeSecretAck{
							NewSecret: newSecret,
						},
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
					log.Error("error responding:", err)
				}
				return

			case *pb.ReqEnvelope_ReqIsDomainAvailable:
				// Issue #103: answers "could a new user take this
				// subdomain?" for a device about to provision one. A
				// one-off request/response like the two cases below, so
				// the connection closes rather than becoming a relay.
				//
				// Authenticated as an existing registered device, not open
				// to all comers - see the message's own doc comment for
				// why an anonymous version of this would be a subdomain
				// enumeration oracle. The answer is deliberately the same
				// shape either way (available true/false), never "taken by
				// <owner>": a caller learns only whether the name it
				// wants is free, which is all it needs to decide.
				defer conn.Close()
				req := p.ReqIsDomainAvailable
				defined, validSecret, err := mg.dao.IsValidDevice(req.OwnerUuid, req.Domain, req.Secret)
				if err != nil && err != sql.ErrNoRows {
					log.Error("error validating device for domain availability check:", err)
					resp.Error = true
					resp.ErrorMessage = "error checking domain"
				} else if !defined || !validSecret {
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
				} else if registered, err := mg.dao.IsDomainRegistered(req.CandidateDomain); err != nil {
					log.Error("error checking whether", req.CandidateDomain, "is registered:", err)
					resp.Error = true
					resp.ErrorMessage = "error checking domain"
				} else {
					resp.Payload = &pb.RespEnvelope_RespDomainAvailable{
						RespDomainAvailable: &pb.RespDomainAvailable{Available: !registered},
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
					log.Error("error responding, closing the connection:", err)
				}
				return

			case *pb.ReqEnvelope_ReqSetDeviceDisabled:
				// Issue #93: the primary telling the bridge one of its own
				// additional users (issue #90) just got disabled/re-enabled -
				// see ReqSetDeviceDisabled's own doc comment for why. One-off
				// request/response, same shape as ReqRotateBridgeSecret above.
				defer conn.Close()
				req := p.ReqSetDeviceDisabled
				defined, validSecret, err := mg.dao.IsValidDevice(req.OwnerUuid, req.Domain, req.Secret)
				if err != nil && err != sql.ErrNoRows {
					log.Error("error validating device for disabled-state update:", err)
					resp.Error = true
					resp.ErrorMessage = err.Error()
				} else if !defined || !validSecret {
					log.Error("disabled-state update rejected: invalid device/secret for", req.Domain)
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), req.Domain, req.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
				} else if err := mg.dao.SetDeviceDisabled(req.Domain, req.Disabled); err != nil {
					log.Error("error storing disabled-state update:", err)
					resp.Error = true
					resp.ErrorMessage = err.Error()
				} else {
					log.Info("Set device disabled:", req.Domain, req.Disabled)
					resp.Payload = &pb.RespEnvelope_RespAck{
						RespAck: &pb.Ack{Ok: true},
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
					log.Error("error responding:", err)
				}
				return

			case *pb.ReqEnvelope_ReqUpdatePushRegistrations:
				// One-off request/response, not a pooled relay connection -
				// same shape as ReqRotateBridgeSecret above (issue #62): the
				// device pushes its full current registration snapshot
				// whenever it changes (and once at startup), the bridge
				// just stores it so it has something to alert with if this
				// device ever goes offline.
				defer conn.Close()
				req := p.ReqUpdatePushRegistrations
				log.Info("Update push registrations for device:", req.Domain)

				defined, validSecret, err := mg.dao.IsValidDevice(req.OwnerUuid, req.Domain, req.Secret)
				if err != nil && err != sql.ErrNoRows {
					log.Error("error validating device for push registration update:", err)
					resp.Error = true
					resp.ErrorMessage = err.Error()
				} else if !defined || !validSecret {
					log.Error("push registration update rejected: invalid device/secret for", req.Domain)
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), req.Domain, req.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
				} else {
					webSubs := make([]push.WebPushSubscription, 0, len(req.WebPushSubs))
					for _, s := range req.WebPushSubs {
						webSubs = append(webSubs, push.WebPushSubscription{Endpoint: s.Endpoint, P256dh: s.P256Dh, Auth: s.Auth})
					}
					if err := mg.dao.SetPushRegistrations(req.Domain, req.VapidPublicKey, req.VapidPrivateKey, req.ApnsTokens, webSubs); err != nil {
						log.Error("error storing push registrations:", err)
						resp.Error = true
						resp.ErrorMessage = err.Error()
					} else {
						resp.Payload = &pb.RespEnvelope_RespUpdatePushRegistrationsAck{
							RespUpdatePushRegistrationsAck: &pb.UpdatePushRegistrationsAck{Ok: true},
						}
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
					log.Error("error responding:", err)
				}
				return

			default:
				defer conn.Close()
				// Issue #93: a disabled additional user (issue #90) has its
				// own process actually stopped, so its pool would just look
				// like any other offline device below - checked first so a
				// login attempt (or any other direct WS request - a native
				// app, say, which never goes through serveStatic's own
				// disabled-page check at all) gets a real explanation
				// instead of a generic connection failure indistinguishable
				// from "temporarily unreachable".
				if disabled, err := mg.dao.IsDeviceDisabled(r.Host); err != nil {
					log.Error("error checking disabled state for", r.Host, ":", err)
				} else if disabled {
					resp.Error = true
					resp.ErrorMessage = "This account has been disabled."
					// Both clients' handshake code (web's encryptForConnection,
					// iOS's connectAndAuth) only ever surfaces an error message
					// out of a non-respPubKey reply when that reply's payload
					// is specifically a RespAck - the top-level Error/
					// ErrorMessage fields alone (set above) get silently
					// dropped in favor of a generic "couldn't fetch public
					// key" otherwise, which would defeat the entire point of
					// this message existing.
					resp.Payload = &pb.RespEnvelope_RespAck{
						RespAck: &pb.Ack{Ok: false, ErrorMsg: resp.ErrorMessage, Code: cCodeAccountDisabled},
					}
					respBin, _ := proto.Marshal(resp)
					if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
						log.Error("error responding, closing the connection:", err)
					}
					return
				}

				// This may be a direct request to a device: pick one off
				// the pool and pair with it for the rest of this
				// connection's life (see deviceRelay/the relay != nil
				// branch above) if the domain exists. Pool entries are
				// already-live *deviceRelay values (registered, not
				// wrapped here) - see ReqBridgeRegister above.
				var picked *deviceRelay
				mg.bridgesMu.RLock()
				pool, ok := mg.bridges[r.Host]
				mg.bridgesMu.RUnlock()
				if ok {
					pool.lock.Lock()
					for picked == nil && len(pool.availableConns) > 0 {
						candidate := pool.availableConns[0]
						pool.availableConns = pool.availableConns[1:]

						log.Debug("Connecting")
						respFrame, err := candidate.forward(frame)
						if err != nil {
							log.Error("Error fordwading message:", err)
							candidate.Close()
							continue
						}
						log.Debug("Connected")

						picked = candidate
						relay = candidate
						// Single use connection, close as soon as it is
						// finished since they are authenticated. Close()
						// triggers candidate's onDeath exactly once (via
						// failAll), decrementing liveCount the same way a
						// genuine network failure would - see
						// deviceRelay.onDeath's doc comment.
						defer relay.Close()

						if err := conn.WriteMessage(gorilla.BinaryMessage, respFrame); err != nil {
							log.Error("error responding, closing the connection:", err)
							pool.lock.Unlock()
							return
						}
						if err := mg.dao.RecordDeviceActivity(r.Host, int64(len(frame)), int64(len(respFrame))); err != nil {
							log.Error("error recording device activity:", err)
						}
					}
					pool.lock.Unlock()
				}

				if picked == nil {
					// Issue #56: this is the one thing standing between a
					// visitor and an explanation when a registered domain's
					// device isn't connected - the HTTP side has its own
					// page for this (issue #97's unavailable.html), but a
					// native app, or a browser tab that was already open
					// when the device went away, only ever reaches here.
					log.Error("no available connections in the pool for", r.Host)
					resp.Error = true
					resp.ErrorMessage = cDeviceUnreachableMsg
					// Wrapped in a RespAck for the same reason the disabled
					// check above is: both clients' handshake code only
					// surfaces an error out of a non-respPubKey reply when
					// the payload is specifically a RespAck, so the
					// top-level Error/ErrorMessage set just above would
					// otherwise be dropped in favour of a generic "couldn't
					// fetch public key" - which is exactly how this used to
					// present, and why an offline device was
					// indistinguishable from a broken one.
					resp.Payload = &pb.RespEnvelope_RespAck{
						RespAck: &pb.Ack{Ok: false, ErrorMsg: resp.ErrorMessage, Code: cCodeDeviceUnreachable},
					}
					respBin, _ := proto.Marshal(resp)
					if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
						log.Error("error responding, closing the connection:", err)
						return
					}
				}
			}
		}
	}
}
