// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/bridge/cluster"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/limits"
	"github.com/alonsovidales/otc/bridge/mailer"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/alonsovidales/otc/wsframe"
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
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
	// relays is every live relay, idle or paired with a client, so that
	// dropping a domain reaches the paired ones too.
	relays    map[*deviceRelay]struct{}
	liveCount int
	// offlineTimer is non-nil exactly while a countdown is pending -
	// started the instant liveCount drops to zero, stopped/cleared the
	// instant a fresh registration brings it back above zero. If it fires
	// with liveCount still at zero, the device is treated as offline.
	offlineTimer *time.Timer
	lock         *sync.Mutex
}

// size is how many idle connections the pool holds; 0 for no pool.
func (p *bridgePool) size() int {
	if p == nil {
		return 0
	}
	p.lock.Lock()
	defer p.lock.Unlock()
	return len(p.availableConns)
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

// cUnpairedReadTimeout bounds each read on a connection not yet paired
// with a device - the wait for a message and the message itself. Every
// real peer sends its first message as soon as the socket opens (the web's
// DeviceUnreachable screen re-asks every 5 s on the same socket); without
// it, anyone could hold sockets open forever, each with up to cFree of an
// unfinished first frame. Long enough for a 2 MB log upload on a slow
// uplink. Var so tests can shrink it.
var cUnpairedReadTimeout = 2 * time.Minute

type deviceRelay struct {
	conn    *gorilla.Conn
	writeMu sync.Mutex // gorilla tolerates only one concurrent writer
	// owner is the owner uuid the device registered with (never its
	// secret): a relay whose owner is no longer the domain's has been
	// replaced by a new identity and must stop relaying.
	owner string

	mu      sync.Mutex
	waiters map[int32]chan deviceReply
	// budget bounds the memory of the replies read here (replyBudget).
	budget *wsframe.Budget

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
	d := newIdleDeviceRelay(conn, onDeath)
	d.start()
	return d
}

// newIdleDeviceRelay is newDeviceRelay without starting its loops (start),
// so a registration can answer the device before anything else may write
// to conn or the relay's death can be counted.
func newIdleDeviceRelay(conn *gorilla.Conn, onDeath func()) *deviceRelay {
	d := &deviceRelay{conn: conn, waiters: make(map[int32]chan deviceReply), budget: replyBudget, onDeath: onDeath, stopPing: make(chan struct{})}

	// The registration was read with cUnpairedReadLimit, and gorilla keeps
	// a connection's limit: from here on this connection carries the
	// device's replies - a whole photo for a preview, a file download -
	// which are bounded like relayed requests. Left at 8 MB, every bigger
	// reply killed the relay and the client saw the device as away.
	conn.SetReadLimit(cRelayedReadLimit)
	// A device's writes have no deadline (its pong deadline closes a dead
	// one); none may be left from a reply written while it was unpaired.
	conn.SetWriteDeadline(time.Time{})

	conn.SetReadDeadline(time.Now().Add(cPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(cPongWait))
		return nil
	})
	return d
}

// start runs the relay's readLoop and pingLoop.
func (d *deviceRelay) start() {
	d.wg.Add(2)
	go func() { defer d.wg.Done(); d.readLoop() }()
	go func() { defer d.wg.Done(); d.pingLoop() }()
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
			// WriteControl, not writeMu: it may go out between the frames
			// of a large request (see writeFrame), where waiting for the
			// whole upload let the pong deadline kill a working relay.
			err := d.conn.WriteControl(gorilla.PingMessage, nil, time.Now().Add(cPongWait))
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// No turn to write within cPongWait: the read deadline
				// decides whether the device is gone, not this.
				continue
			}
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

// deviceReply is a device's response frame and the release of its share
// of replyBudget, which whoever ends up with it calls once done.
type deviceReply struct {
	frame   []byte
	release func()
}

// readLoop is this relay's one and only reader — gorilla tolerates only
// one concurrent reader, same as one writer — so every response coming
// back from the device passes through here and gets routed to whichever
// forward() call is waiting on that response's envelope id.
func (d *deviceRelay) readLoop() {
	for {
		// Budgeted like the clients' frames (issue #163): replies of up to
		// cRelayedReadLimit, on every relay at once, idle ones included,
		// were otherwise buffered whole with nothing bounding the total.
		_, frame, release, err := wsframe.Read(d.conn, cRelayedReadLimit, d.budget)
		if err != nil {
			d.failAll()
			return
		}
		id, err := envelopeID(frame)
		if err != nil {
			release()
			log.Error("bad proto from device:", err)
			continue
		}
		d.mu.Lock()
		ch, ok := d.waiters[id]
		if ok {
			delete(d.waiters, id)
			// Sent under d.mu (the channel has room for it), so abandon
			// knows a reply is either in ch or never coming.
			ch <- deviceReply{frame, release}
		}
		d.mu.Unlock()
		if !ok {
			// No waiter for this id (already gave up, or a stray/duplicate
			// message) — nothing to deliver it to, so just drop it.
			release()
		}
	}
}

// failAll unblocks every still-pending forward() call once the device
// connection itself has died, instead of leaving each one hanging forever
// waiting on a response that will now never arrive.
func (d *deviceRelay) failAll() {
	d.mu.Lock()
	waiters := d.waiters
	d.waiters = make(map[int32]chan deviceReply)
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
// clientAddr (issue #117) is the address reported to a device for the
// client on this connection. The bridge sits behind TLS termination on the
// same host, so X-Forwarded-For's first hop is the real client when the
// connection came from loopback; otherwise the peer itself.
func clientAddr(r *http.Request, conn *gorilla.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		host = conn.RemoteAddr().String()
	}
	// Issue #144: or from another bridge node, which says who the client
	// is - the internal listener only sets HopHeader once the cluster token
	// checked out, and the public listeners strip it.
	if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || r.Header.Get(cluster.HopHeader) == "1" {
		if fwd := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); fwd != "" {
			return fwd
		}
	}
	return host
}

// envelopeID reads a Req/RespEnvelope's id (field 1) without decoding
// the rest (issue #163): relaying a frame never needs its payload, and a
// full proto.Unmarshal of a large file chunk just to correlate it cost a
// second copy of it.
//
// The last id wins, as in proto.Unmarshal on the device: taking the first
// one let a frame carrying two ids be waited on under one while the device
// answered the other, and every relay it was tried on timed out. A
// malformed tail after an id still answers that id, as before.
func envelopeID(frame []byte) (int32, error) {
	var id int32
	found := false
	fail := func(n int) (int32, error) {
		if found {
			return id, nil
		}
		return 0, protowire.ParseError(n)
	}
	b := frame
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fail(n)
		}
		b = b[n:]
		if num == 1 && typ == protowire.VarintType {
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return fail(m)
			}
			id, found = int32(v), true
			b = b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return fail(m)
		}
		b = b[m:]
	}
	return id, nil // no id field: proto3's default, 0
}

// The returned release gives the reply's share of replyBudget back: call
// it once the reply is written on. Never nil.
func (d *deviceRelay) forward(frame []byte) ([]byte, func(), error) {
	return d.forwardWithTimeout(frame, cForwardTimeout)
}

func noRelease() {}

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
func (d *deviceRelay) forwardWithTimeout(frame []byte, timeout time.Duration) ([]byte, func(), error) {
	id, err := envelopeID(frame)
	if err != nil {
		return nil, noRelease, fmt.Errorf("bad proto: %w", err)
	}

	ch := make(chan deviceReply, 1)
	d.mu.Lock()
	d.waiters[id] = ch
	d.mu.Unlock()

	err = d.writeFrame(frame)
	if err != nil {
		d.abandon(id, ch)
		return nil, noRelease, err
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
			return nil, noRelease, errors.New("device connection closed")
		}
		return resp.frame, resp.release, nil
	case <-time.After(timeout):
		d.abandon(id, ch)
		return nil, noRelease, fmt.Errorf("timed out waiting for device response")
	}
}

// abandon gives up waiting on ch. A reply readLoop already handed over in
// the meantime is released, or its budget share would be lost for good.
func (d *deviceRelay) abandon(id int32, ch chan deviceReply) {
	d.mu.Lock()
	if d.waiters[id] == ch {
		delete(d.waiters, id)
	}
	d.mu.Unlock()
	select {
	case r, ok := <-ch:
		if ok {
			r.release()
		}
	default:
	}
}

// cRelayWriteChunk: a request larger than this goes to the device as a
// fragmented message of chunks this size, so pingLoop's ping can get out
// between them. Written whole, a several-hundred-MB upload held the socket
// past cPongWait and the relay was declared dead mid-transfer.
const cRelayWriteChunk = 256 << 10

// writeFrame writes one request to the device. writeMu keeps concurrent
// forwards from interleaving their frames; gorilla serialises the ping
// between chunks itself.
func (d *deviceRelay) writeFrame(frame []byte) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if len(frame) <= cRelayWriteChunk {
		return d.conn.WriteMessage(gorilla.BinaryMessage, frame) // one frame, as always
	}
	w, err := d.conn.NextWriter(gorilla.BinaryMessage)
	if err != nil {
		return err
	}
	for len(frame) > 0 {
		n := min(len(frame), cRelayWriteChunk)
		if _, err := w.Write(frame[:n]); err != nil {
			return err
		}
		frame = frame[n:]
	}
	return w.Close()
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
	baseUrl string
	dao     *dao.Dao
	// openRegistration is [accounts] open-registration (issue #124): an
	// unknown device dialling in registers its domain on the spot. Off in
	// production, where domains come from accounts.
	openRegistration bool
	upgrader         gorilla.Upgrader
	bridges          map[string]*bridgePool // The domain is the key and the value the pool of connections
	// oneOffSlots: domain -> chan struct{} of cOneOffConcurrent (issue #163).
	oneOffSlots sync.Map
	// mailer and logsPerDomain: devices' "Send logs to us".
	mailer        *mailer.Mailer
	logsPerDomain *limits.Rate
	bridgesMu     sync.RWMutex // guards the bridges map itself, not each pool's own contents (pool.lock does that)

	// Issue #144: nil on a single bridge. dirty queues the domains whose
	// claim in Redis may have to change; one goroutine (clusterSync)
	// applies them, so a claim and its release are never reordered.
	cluster *cluster.Cluster
	dirty   chan string
}

// SetCluster makes this bridge one node of c (issue #144): it claims in
// Redis every device it holds connections to, and answers "online" for
// devices other nodes hold.
func (mg *Manager) SetCluster(c *cluster.Cluster) {
	if c == nil {
		return
	}
	mg.cluster = c
	mg.dirty = make(chan string, 4096)
	go mg.clusterSync()
	// Outside clusterSync: closing relays must never hold up the claims.
	c.SubscribeDrops(mg.dropPool)
}

// markDirty asks clusterSync to bring domain's claim up to date. Never
// blocks: with the queue full, the next refresh does it.
func (mg *Manager) markDirty(domain string) {
	if mg.dirty == nil {
		return
	}
	select {
	case mg.dirty <- domain:
	default:
	}
}

func (mg *Manager) clusterSync() {
	if err := mg.cluster.Announce(); err != nil {
		log.Error("cluster: could not announce this node:", err)
	}
	t := time.NewTicker(cluster.Refresh)
	defer t.Stop()
	for {
		select {
		case d := <-mg.dirty:
			var err error
			if mg.liveCount(d) > 0 {
				err = mg.cluster.Hold(d)
			} else {
				err = mg.cluster.Release(d)
			}
			if err != nil {
				log.Error("cluster: could not update the claim on", d, ":", err)
			}
		case <-t.C:
			if err := mg.cluster.Announce(); err != nil {
				log.Error("cluster: could not announce this node:", err)
			}
			if err := mg.cluster.Hold(mg.heldDomains()...); err != nil {
				log.Error("cluster: could not refresh the claims:", err)
			}
		}
	}
}

// liveCount is how many live connections this node has to domain.
func (mg *Manager) liveCount(domain string) int {
	mg.bridgesMu.RLock()
	pool, ok := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if !ok {
		return 0
	}
	pool.lock.Lock()
	defer pool.lock.Unlock()
	return pool.liveCount
}

// heldDomains is every device this node has live connections to.
func (mg *Manager) heldDomains() []string {
	mg.bridgesMu.RLock()
	pools := make(map[string]*bridgePool, len(mg.bridges))
	for d, p := range mg.bridges {
		pools[d] = p
	}
	mg.bridgesMu.RUnlock()
	var out []string
	for d, p := range pools {
		p.lock.Lock()
		if p.liveCount > 0 {
			out = append(out, d)
		}
		p.lock.Unlock()
	}
	return out
}

// HasLocal reports whether this node has a free connection to domain's
// device - one a new client can be paired with here. Otherwise a client
// is better served by a node that has (api.clusterRoute).
func (mg *Manager) HasLocal(domain string) bool {
	mg.bridgesMu.RLock()
	pool, ok := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if !ok {
		return false
	}
	pool.lock.Lock()
	defer pool.lock.Unlock()
	return len(pool.availableConns) > 0
}

func Init(baseUrl string, dao *dao.Dao) (mg *Manager) {
	mg = &Manager{
		logsPerDomain:    limits.NewRate(3.0/3600, 3),
		baseUrl:          baseUrl,
		dao:              dao,
		openRegistration: cfg.HasSection("accounts") && cfg.GetStr("accounts", "open-registration") == "true",
		upgrader: gorilla.Upgrader{
			// In production, set a proper origin check!
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		bridges: make(map[string]*bridgePool),
	}
	if dao != nil {
		go mg.sweepLoop()
	}

	return
}

// cSweepEvery is how often sweepStale runs.
var cSweepEvery = cluster.Refresh

func (mg *Manager) sweepLoop() {
	t := time.NewTicker(cSweepEvery)
	defer t.Stop()
	for range t.C {
		mg.sweepStale()
	}
}

// sweepStale closes this node's relays for domains no longer registered,
// and those registered by an owner uuid the domain no longer has: what a
// drop published while this node was cut off from Redis, a node on an
// older release, or a deletion that drops nothing (the admin panel's)
// leaves relaying. Its own goroutine, not clusterSync's: a slow query
// must not hold up the claims. A database error closes nothing.
func (mg *Manager) sweepStale() {
	held := mg.heldDomains()
	if len(held) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owners, err := mg.dao.DeviceOwners(ctx, held)
	if err != nil {
		log.Error("could not check the held domains against the database:", err)
		return
	}
	for _, d := range held {
		owner, ok := owners[d]
		if !ok {
			// Not stored under exactly this name: ask the way the
			// registration was checked before calling it gone.
			var registered bool
			if owner, registered, err = mg.dao.DeviceOwner(ctx, d); err != nil {
				log.Error("could not check", d, "against the database:", err)
				return
			}
			if !registered {
				log.Info("closing the connections of a domain no longer registered:", d)
				mg.dropPool(d)
				continue
			}
		}
		if n := mg.dropRelays(d, func(r *deviceRelay) bool { return r.owner != owner }); n > 0 {
			log.Info("closed", n, "connections of a replaced identity for", d)
		}
	}
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
func (s *domainPushStorage) ListFcmTokens() ([]string, error) {
	return s.dao.ListFcmTokensForDomain(s.domain)
}
func (s *domainPushStorage) DeleteFcmToken(t string) error {
	return s.dao.DeleteFcmTokenForDomain(s.domain, t)
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
	ps.Notify("Off The Cloud", "Your device appears to have gone offline", push.Target{})
}

// onDeviceConnectionRegistered records that domain just gained one more
// live bridge connection (a fresh ReqBridgeRegister) - cancelling any
// pending offline countdown, since the device is provably reachable again.
// Must be called with pool.lock held.
func (mg *Manager) onDeviceConnectionRegistered(domain string, pool *bridgePool) {
	pool.liveCount++
	if pool.liveCount == 1 {
		mg.markDirty(domain)
	}
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
func (mg *Manager) onDeviceConnectionDied(domain string, dead *deviceRelay) {
	mg.bridgesMu.RLock()
	pool, ok := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if !ok {
		return
	}

	pool.lock.Lock()
	defer pool.lock.Unlock()

	// Drop it from the idle list as well as the count. Without this the
	// entry stayed in availableConns forever once its connection died -
	// a device restart, a network blip, a ping/pong timeout - and the
	// consequences compounded:
	//
	//   - every client request popped corpses first, each costing a full
	//     cForwardTimeout (90s) before moving on, so a page load could
	//     hang for minutes;
	//   - ForwardOneOff hit the same, which is what surfaced as "no
	//     available connections in the pool" while the device itself was
	//     perfectly healthy;
	//   - and because the registration cap counts len(availableConns),
	//     100 accumulated corpses meant every *new* registration was
	//     rejected ("device at its connection cap"), leaving the device
	//     permanently unreachable until the bridge process restarted.
	//
	// All three were live on cala/tobi: tobi was being refused 2264 times
	// in three minutes against a pool that was entirely dead.
	delete(pool.relays, dead)
	if dead != nil {
		for i, c := range pool.availableConns {
			if c == dead {
				pool.availableConns = append(pool.availableConns[:i], pool.availableConns[i+1:]...)
				break
			}
		}
	}

	if pool.liveCount > 0 {
		pool.liveCount--
	}
	if pool.liveCount == 0 {
		mg.markDirty(domain)
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

	// Issue #144: gone from this node isn't gone - the device may hold
	// connections to another one, whose own countdown covers it.
	if stillDown && mg.cluster.HeldElsewhere(domain) {
		log.Info("device left this node but another one holds it, no offline alert:", domain)
		return
	}
	if stillDown {
		mg.sendOfflineAlert(domain)
	}
}

// cClientWriteChunk/cClientWriteStall: a write to a client times out on a
// lack of progress, not on its size. A reply larger than a chunk goes out
// as a fragmented message, each chunk with its own deadline, so a slow
// link never trips it and a client that stopped reading (a zero window, a
// peer gone without a FIN) does within cClientWriteStall - instead of
// pinning its relay goroutines, their frames and its device connection.
const cClientWriteChunk = 256 << 10

var cClientWriteStall = 60 * time.Second // var so tests can shrink it

// writeClient writes msg to a client's socket. The caller holds the
// socket's writer slot (writeMu, or is its reading goroutine before
// pairing). Never a device's socket: the deadline would stay on it.
func writeClient(conn *gorilla.Conn, msg []byte) error {
	if len(msg) <= cClientWriteChunk {
		conn.SetWriteDeadline(time.Now().Add(cClientWriteStall))
		return conn.WriteMessage(gorilla.BinaryMessage, msg) // one frame, as always
	}
	w, err := conn.NextWriter(gorilla.BinaryMessage)
	if err != nil {
		return err
	}
	for len(msg) > 0 {
		n := min(len(msg), cClientWriteChunk)
		conn.SetWriteDeadline(time.Now().Add(cClientWriteStall))
		if _, err := w.Write(msg[:n]); err != nil {
			return err
		}
		msg = msg[n:]
	}
	conn.SetWriteDeadline(time.Now().Add(cClientWriteStall))
	return w.Close()
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
	if err := writeClient(conn, resp); err != nil {
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

// cPairMaxAttempts caps the pool connections a client's first request may
// spend before it is told the device is unreachable, for the same reason
// (see TestForwardOneOffCannotDrainThePool): each failed attempt closes
// the connection it tried.
const cPairMaxAttempts = cOneOffMaxAttempts

// cOneOffConcurrent caps the one-off requests (static assets, /media)
// in flight per device (issue #163). Each spends a pool connection, and
// the device refills its pool a couple at a time: unbounded, a burst of
// GETs - a page load, or anyone hammering a device's address - emptied
// the pool and left the device's real clients with "no connection".
// Waiting past cOneOffSlotWait answers "busy" instead.
const cOneOffConcurrent = 3

var cOneOffSlotWait = 10 * time.Second

// cOneOffConnWait: how long a one-off request waits for a free pool
// connection. A page load spends several at once (each one-off connection
// is closed after use) and the device opens replacements a couple at a
// time within a moment, so failing at once showed up as a missing logo or
// script on a busy page.
var cOneOffConnWait = 5 * time.Second

// takeAvailable pops a free connection off the pool, waiting up to wait
// for the device to open one; nil if none came.
func takeAvailable(pool *bridgePool, wait time.Duration) *deviceRelay {
	deadline := time.Now().Add(wait)
	for {
		pool.lock.Lock()
		if len(pool.availableConns) > 0 {
			c := pool.availableConns[0]
			pool.availableConns = pool.availableConns[1:]
			pool.lock.Unlock()
			return c
		}
		pool.lock.Unlock()
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// cMaxLogsBytes bounds a device's gzip'd logs (Settings > Logs).
const cMaxLogsBytes = 2 << 20

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// SetMailer lets "Send logs" reach the project (main, after [smtp]).
func (mg *Manager) SetMailer(m *mailer.Mailer) { mg.mailer = m }

// CodeDomainNotRegistered is the RespEnvelope.error_code a device gets
// when it dials in with a name the bridge doesn't know (issue #182).
const CodeDomainNotRegistered = "domain_not_registered"

// dropPool closes every connection domain's device holds on this node,
// idle or paired with a client, once its name is gone: they would go on
// relaying for a name that is no longer its (issue #182).
func (mg *Manager) dropPool(domain string) {
	mg.dropRelays(domain, func(*deviceRelay) bool { return true })
}

// dropRelays closes the relays of domain's pool that match. Never under
// pool.lock: Close waits for readLoop, whose onDeath takes it.
func (mg *Manager) dropRelays(domain string, match func(*deviceRelay) bool) int {
	mg.bridgesMu.RLock()
	pool, ok := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if !ok {
		return 0
	}
	pool.lock.Lock()
	var doomed []*deviceRelay
	for r := range pool.relays {
		if match(r) {
			doomed = append(doomed, r)
		}
	}
	kept := pool.availableConns[:0]
	for _, r := range pool.availableConns {
		if !match(r) {
			kept = append(kept, r)
		} else if _, listed := pool.relays[r]; !listed {
			doomed = append(doomed, r)
		}
	}
	clear(pool.availableConns[len(kept):])
	pool.availableConns = kept
	pool.lock.Unlock()
	for _, r := range doomed {
		r.Close()
	}
	return len(doomed)
}

// evictOtherOwners closes domain's relays registered by another owner
// than owner - a replaced identity's - as owner's device registers, and
// returns how many idle connections the pool has left (the cap counts
// those). The old device's relays never count against the new one's.
func (mg *Manager) evictOtherOwners(domain string, pool *bridgePool, owner string) int {
	if pool == nil {
		return 0
	}
	if n := mg.dropRelays(domain, func(r *deviceRelay) bool { return r.owner != owner }); n > 0 {
		log.Info("closed", n, "connections of a replaced identity for", domain)
	}
	return pool.size()
}

// DropDomains is dropPool for each domain (an account's, when it is
// deleted; one whose identity was replaced) - on every node of the
// cluster, which holds connections from the same device too.
func (mg *Manager) DropDomains(domains []string) {
	if mg == nil {
		return
	}
	for _, d := range domains {
		mg.dropPool(d)
	}
	if mg.cluster.Enabled() && len(domains) > 0 {
		go func() {
			if err := mg.cluster.PublishDrop(domains...); err != nil {
				log.Error("cluster: could not tell the other nodes to drop", domains, ":", err)
			}
		}()
	}
}

// cInternalErrorMsg is what a client is told when the bridge's own store
// fails; the error itself is logged, never sent (issue #163).
const cInternalErrorMsg = "The bridge could not do this right now, try again later"

// ErrDeviceBusy is ForwardOneOff's answer when the device already has
// cOneOffConcurrent one-off requests in flight for longer than
// cOneOffSlotWait.
var ErrDeviceBusy = errors.New("the device is busy, try again shortly")

func (mg *Manager) oneOffSlot(domain string) chan struct{} {
	ch, _ := mg.oneOffSlots.LoadOrStore(domain, make(chan struct{}, cOneOffConcurrent))
	return ch.(chan struct{})
}

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
// IsOnline reports whether domain's device currently holds at least one
// live connection to this bridge (issue #38: the setup wizard asks this
// after installing, to know the device it just set up is really reachable
// before sending the person to its address).
// Issue #144: on any node of the cluster.
func (mg *Manager) IsOnline(domain string) bool {
	return mg.liveCount(domain) > 0 || mg.cluster.Online(domain)
}

func (mg *Manager) ForwardOneOff(domain string, frame []byte) (respFrame []byte, err error) {
	mg.bridgesMu.RLock()
	pool, ok := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if !ok {
		return nil, errors.New("device is offline")
	}

	slot := mg.oneOffSlot(domain)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-time.After(cOneOffSlotWait):
		return nil, ErrDeviceBusy
	}

	for attempt := 0; attempt < cOneOffMaxAttempts; attempt++ {
		candidate := takeAvailable(pool, cOneOffConnWait)
		if candidate == nil {
			return nil, errors.New("no available connections in the pool for this device")
		}

		var release func()
		respFrame, release, err = candidate.forwardWithTimeout(frame, cOneOffForwardTimeout)
		candidate.Close()
		if err != nil {
			log.Error("error forwarding one-off request, trying the next available connection:", err)
			continue
		}
		// One-off replies are few (cOneOffConcurrent a device) and bounded
		// by the device: budgeted while read, not while the caller writes.
		release()
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
	id, err := envelopeID(reqFrame)
	if err != nil {
		return nil, err
	}
	return deviceUnreachableReply(id)
}

// deviceUnreachableReply is deviceUnreachableFrame for a request id.
func deviceUnreachableReply(id int32) ([]byte, error) {
	return proto.Marshal(&pb.RespEnvelope{
		Id:           id,
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

	inFlight := make(chan struct{}, cMaxInFlight)
	// conn is closed on every way out of here except a registered device's,
	// whose relay owns it from then on: a refused registration, a read
	// error or an undecodable first frame used to leave the socket open
	// with nothing reading it (CLOSE_WAIT until a GC finalizer). Deferred
	// after wg.Wait so it runs first: closing unblocks a relay goroutine
	// stuck writing to a client that stopped reading.
	handedOff := false
	defer func() {
		if !handedOff {
			conn.Close()
		}
	}()

	for {
		// Small until the client is paired with a device (its first
		// request, a device's registration); a relayed request may carry a
		// whole file (up to 1000 MB) and is budgeted (see wsframe).
		limit := int64(cUnpairedReadLimit)
		if relay != nil {
			limit = cRelayedReadLimit
		} else {
			// Paired clients may sit idle as long as they like: the
			// deadline is cleared when the pairing happens.
			conn.SetReadDeadline(time.Now().Add(cUnpairedReadTimeout))
		}
		_, frame, releaseFrame, err := wsframe.Read(conn, limit, frameBudget)
		if err != nil {
			var ne net.Error
			if relay == nil && errors.As(err, &ne) && ne.Timeout() {
				// A background tab, mostly: not worth an error line each.
				log.Debug("closing an unpaired connection that went quiet:", err)
			} else {
				log.Error("error processing message:", err)
			}
			return
		}

		// Only the bridge says who a client is (BridgeClientInfo, sent
		// once when it pairs the client with a relay). The same message
		// from a client would let it pick the address the device's
		// password-attempt limit is kept for.
		if isClientInfo(frame) {
			log.Info("dropped a BridgeClientInfo sent by a client")
			releaseFrame()
			continue
		}

		if relay != nil {
			inFlight <- struct{}{}
			wg.Add(1)
			go func(frame []byte) {
				defer wg.Done()
				defer func() { <-inFlight }()
				defer releaseFrame()
				// Mirrors handleConnection's own top-level recover: this
				// now runs on its own goroutine, which the outer recover
				// above can't reach — an unrecovered panic here would
				// otherwise still take the whole process down.
				defer func() {
					if r := recover(); r != nil {
						log.Error("recovered from panic relaying message:", r, string(debug.Stack()))
					}
				}()

				reqLen := int64(len(frame))
				reqID, idErr := envelopeID(frame)
				respFrame, releaseResp, err := relay.forward(frame)
				defer releaseResp()
				// The request is on the device now: its budget share and
				// memory aren't needed while the reply goes out, which a
				// slow client can stretch out.
				frame = nil
				releaseFrame()
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
					if idErr != nil {
						log.Error("error building unreachable response:", idErr)
						return
					}
					unreachableFrame, mErr := deviceUnreachableReply(reqID)
					if mErr != nil {
						log.Error("error building unreachable response:", mErr)
						return
					}
					writeMu.Lock()
					wErr := writeClient(conn, unreachableFrame)
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
				writeErr := writeClient(conn, respFrame)
				writeMu.Unlock()
				if writeErr != nil {
					// Closing is what ends the session: gorilla only records a
					// failed write, and the reader would go on waiting on a
					// client that stopped reading, its relay claimed.
					log.Error("error forwading respose, closing the connection:", writeErr)
					conn.Close()
					return
				}

				if err := mg.dao.RecordDeviceActivity(r.Host, reqLen, int64(len(respFrame))); err != nil {
					// Metrics are best-effort: never fail the actual relay
					// over a metrics-write error.
					log.Error("error recording device activity:", err)
				}
			}(frame)
		} else {
			// Unpaired: a small message (see cUnpairedReadLimit), handled
			// right here - its share of the budget isn't needed past this.
			releaseFrame()
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
					// Issue #124: a domain is registered by its account -
					// the setup wizard's claim or the account page - not
					// by whoever dials in first with it. Only [accounts]
					// open-registration keeps the old first-come rule.
					if mg.openRegistration {
						err = mg.dao.RegistreDevice(p.ReqBridgeRegister.OwnerUuid, domain, p.ReqBridgeRegister.Secret)
					} else {
						log.Error("rejected registration for", domain, "from", conn.RemoteAddr().String(), "- the domain is not registered to any account")
						resp.Error = true
						resp.ErrorMessage = "This domain is not registered: create an account at https://" + mg.baseHost() + "/account and add it there, or run the setup again signed in"
						// Issue #182: a device whose name stays unknown here
						// (its account deleted, the name released) goes on
						// working locally.
						resp.ErrorCode = CodeDomainNotRegistered
						if logErr := mg.dao.LogAuthEvent(uuid.New().String(), domain, p.ReqBridgeRegister.OwnerUuid, conn.RemoteAddr().String(), "unregistered_domain"); logErr != nil {
							log.Error("error logging auth event:", logErr)
						}
						respBin, _ := proto.Marshal(resp)
						if err := writeClient(conn, respBin); err != nil {
							log.Error("error responding:", err)
						}
						conn.Close()
						return
					}
				}

				mg.bridgesMu.RLock()
				pool, ok := mg.bridges[domain]
				mg.bridgesMu.RUnlock()

				if err != nil {
					// Checked first: IsValidDevice answers a database
					// failure with defined=true and validSecret=false, which
					// is not a wrong secret - no "Invalid Secret", no
					// invalid_secret auth event; the device backs off and
					// retries. Also a failed open-registration insert.
					log.Error("error checking registration for", domain, "from", conn.RemoteAddr().String(), ":", err)
					resp.Error = true
					resp.ErrorMessage = cInternalErrorMsg
				} else if defined && !validSecret {
					// err is nil on this branch (checked just above: the
					// answer was "no"), so it used to log a useless "error
					// registering bridge: <nil>" with no domain, which sent
					// an outage investigation down the wrong path. The
					// secret itself is deliberately not logged.
					log.Error("rejected registration for", domain, "from", conn.RemoteAddr().String(),
						"- owner/secret do not match the bridge's record (owner claimed:", p.ReqBridgeRegister.OwnerUuid, ")")
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
					// Someone tried to register an already-claimed domain with
					// the wrong owner_uuid/secret - could be a misconfigured
					// device, or someone probing for a weak/leaked secret.
					// Surfaced in the admin panel's security log (issue #8).
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), domain, p.ReqBridgeRegister.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
				} else if size := mg.evictOtherOwners(domain, pool, p.ReqBridgeRegister.OwnerUuid); ok && size >= maxConnectionsPerDevice() {
					// Issue #53 follow-up: a device now grows its own pool
					// dynamically under load (see websocket.ensureBridgePool
					// on the device side) rather than dialing a fixed count
					// once - this is the backstop against that (or anything
					// else) growing one device's pool unbounded.
					log.Error("device at its connection cap, rejecting:", domain, size)
					resp.Error = true
					resp.ErrorMessage = "Device connection pool is full"
				} else {
					// The relay (and its read loop) is created right here,
					// at registration time, rather than lazily once picked -
					// see newDeviceRelay's doc comment for why that matters
					// to issue #62's offline detection.
					// The closure captures `relay` by reference and only
					// ever runs later (onDeath fires from failAll), by
					// which point the assignment below has completed - so
					// the handler always has the relay it belongs to, and
					// can evict that exact entry from the pool.
					var relay *deviceRelay
					relay = newIdleDeviceRelay(conn, func() { mg.onDeviceConnectionDied(domain, relay) })
					relay.owner = p.ReqBridgeRegister.OwnerUuid

					// The ack goes out before the relay is in the pool. Once
					// it is, a client's pairing or a one-off can write to
					// conn: written after, the ack raced that write (two
					// writers on one gorilla conn) or came second, and the
					// device took the client's request for its answer.
					resp.Payload = &pb.RespEnvelope_RespBridgeAckOnboard{
						RespBridgeAckOnboard: &pb.BridgeAckOnboard{
							Ok: true,
						},
					}
					respBin, _ := proto.Marshal(resp)
					if err := conn.WriteMessage(gorilla.BinaryMessage, respBin); err != nil {
						log.Error("error responding, closing the connection:", err)
						return
					}
					handedOff = true

					// Re-check under the write lock (rather than trusting
					// the ok/pool snapshot read above) so two connections
					// registering the same brand-new domain at once can't
					// each create their own separate pool for it.
					mg.bridgesMu.Lock()
					pool, ok = mg.bridges[domain]
					if !ok {
						log.Debug("Creating new pool")
						pool = &bridgePool{lock: new(sync.Mutex)}
						mg.bridges[domain] = pool
					}
					mg.bridgesMu.Unlock()
					pool.lock.Lock()
					log.Debug("Adding to the pool:", len(pool.availableConns))
					pool.availableConns = append(pool.availableConns, relay)
					if pool.relays == nil {
						pool.relays = make(map[*deviceRelay]struct{})
					}
					pool.relays[relay] = struct{}{}
					mg.onDeviceConnectionRegistered(domain, pool)
					// Started under the lock: onDeath takes it, so a
					// connection that dies at once is counted out after it
					// was counted in, never before (liveCount stuck at 1).
					relay.start()
					pool.lock.Unlock()
					return
				}

				// Refused: answered here, and conn is closed on the way out.
				respBin, _ := proto.Marshal(resp)
				if err := writeClient(conn, respBin); err != nil {
					log.Error("error responding, closing the connection:", err)
				}
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
				// Issue #157: random bytes, not two UUIDs glued together.
				secretBytes := make([]byte, 32)
				if _, err := rand.Read(secretBytes); err != nil {
					log.Error("crypto/rand failed:", err)
					return
				}
				newSecret := hex.EncodeToString(secretBytes)
				ok, err := mg.dao.RotateSecret(
					p.ReqRotateBridgeSecret.OwnerUuid,
					p.ReqRotateBridgeSecret.Domain,
					p.ReqRotateBridgeSecret.Secret,
					newSecret,
				)
				if err != nil {
					log.Error("error rotating secret:", err)
					resp.Error = true
					resp.ErrorMessage = cInternalErrorMsg
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
				if err := writeClient(conn, respBin); err != nil {
					log.Error("error responding:", err)
				}
				return

			case *pb.ReqEnvelope_ReqBridgeReleaseDomain:
				// Issue #182: the device leaves the bridge. Authenticated by
				// its current secret, like ReqRotateBridgeSecret above.
				defer conn.Close()
				req := p.ReqBridgeReleaseDomain
				ok, err := mg.dao.ReleaseDeviceDomain(req.OwnerUuid, req.Domain, req.Secret)
				switch {
				case err != nil:
					log.Error("error releasing a device's domain:", err)
					resp.Error = true
					resp.ErrorMessage = cInternalErrorMsg
				case !ok:
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), req.Domain, req.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
				default:
					log.Info("device released its domain:", req.Domain)
					mg.DropDomains([]string{req.Domain})
					resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
				}
				respBin, _ := proto.Marshal(resp)
				if err := writeClient(conn, respBin); err != nil {
					log.Error("error responding:", err)
				}
				return

			case *pb.ReqEnvelope_ReqBridgeSendLogs:
				// Settings > Logs > "Send to us": a device's logs, mailed to
				// the project with its owner's email as Reply-To.
				// Authenticated by the device's secret; 3 an hour per device.
				defer conn.Close()
				req := p.ReqBridgeSendLogs
				defined, valid, err := mg.dao.IsValidDevice(req.OwnerUuid, req.Domain, req.Secret)
				switch {
				case err != nil && defined:
					log.Error("error checking a device sending logs:", err)
					resp.Error, resp.ErrorMessage = true, cInternalErrorMsg
				case !defined || !valid:
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), req.Domain, req.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
					resp.Error, resp.ErrorMessage = true, "Invalid Secret"
				case len(req.Logs) > cMaxLogsBytes:
					resp.Error, resp.ErrorMessage = true, "the logs are too big to send"
				case !mg.logsPerDomain.Allow(req.Domain):
					resp.Error, resp.ErrorMessage = true, "logs were sent from this device a few times already - try again in an hour"
				case mg.mailer == nil:
					resp.Error, resp.ErrorMessage = true, "the bridge can't send email right now"
				default:
					owner := mg.dao.AccountEmailForDomain(req.Domain)
					body := fmt.Sprintf("Logs sent from %s for debugging.\nAccount: %s\n\nNote from the owner:\n%s\n", req.Domain, orNone(owner), orNone(strings.TrimSpace(req.Note)))
					att := &mailer.Attachment{Name: "otc-logs-" + req.Domain + ".txt.gz", ContentType: "application/gzip", Data: req.Logs}
					if err := mg.mailer.SendWith(mg.mailer.Self(), owner, "Logs from "+req.Domain, body, att); err != nil {
						log.Error("could not mail a device's logs:", err)
						resp.Error, resp.ErrorMessage = true, "the logs could not be sent right now"
					} else {
						log.Info("logs mailed for", req.Domain)
						resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
					}
				}
				respBin, _ := proto.Marshal(resp)
				if err := writeClient(conn, respBin); err != nil {
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
				if err := writeClient(conn, respBin); err != nil {
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
					resp.ErrorMessage = cInternalErrorMsg
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
					resp.ErrorMessage = cInternalErrorMsg
				} else {
					log.Info("Set device disabled:", req.Domain, req.Disabled)
					resp.Payload = &pb.RespEnvelope_RespAck{
						RespAck: &pb.Ack{Ok: true},
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := writeClient(conn, respBin); err != nil {
					log.Error("error responding:", err)
				}
				return

			// A device asking for an iOS push to its own phones - see
			// BridgeNotify in messages.proto. Authenticated exactly like a
			// registration update, and sent to the tokens stored under the
			// authenticated domain only: the request names none, so there
			// is no way to reach another device's phones from here.
			case *pb.ReqEnvelope_ReqBridgeNotify:
				defer conn.Close()
				req := p.ReqBridgeNotify
				log.Info("Relay push for device:", req.Domain)

				defined, validSecret, err := mg.dao.IsValidDevice(req.OwnerUuid, req.Domain, req.Secret)
				if err != nil && err != sql.ErrNoRows {
					log.Error("error validating device for push relay:", err)
					resp.Error = true
					resp.ErrorMessage = cInternalErrorMsg
				} else if !defined || !validSecret {
					log.Error("push relay rejected: invalid device/secret for", req.Domain)
					if logErr := mg.dao.LogAuthEvent(uuid.New().String(), req.Domain, req.OwnerUuid, conn.RemoteAddr().String(), "invalid_secret"); logErr != nil {
						log.Error("error logging auth event:", logErr)
					}
					resp.Error = true
					resp.ErrorMessage = "Invalid Secret"
				} else if ps, err := push.Init(&domainPushStorage{dao: mg.dao, domain: req.Domain}); err != nil {
					log.Error("could not init push for relay:", req.Domain, err)
					resp.Error = true
					resp.ErrorMessage = cInternalErrorMsg
				} else {
					ps.NotifyMobile(req.Title, req.Body, push.Target{Kind: req.Kind, PubUUID: req.PubUuid, CommentUUID: req.CommentUuid})
					resp.Payload = &pb.RespEnvelope_RespBridgeNotifyAck{
						RespBridgeNotifyAck: &pb.BridgeNotifyAck{Ok: true},
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := writeClient(conn, respBin); err != nil {
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
					resp.ErrorMessage = cInternalErrorMsg
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
					if err := mg.dao.SetPushRegistrations(req.Domain, req.VapidPublicKey, req.VapidPrivateKey, req.ApnsTokens, req.FcmTokens, webSubs); err != nil {
						log.Error("error storing push registrations:", err)
						resp.Error = true
						resp.ErrorMessage = cInternalErrorMsg
					} else {
						resp.Payload = &pb.RespEnvelope_RespUpdatePushRegistrationsAck{
							RespUpdatePushRegistrationsAck: &pb.UpdatePushRegistrationsAck{Ok: true},
						}
					}
				}

				respBin, _ := proto.Marshal(resp)
				if err := writeClient(conn, respBin); err != nil {
					log.Error("error responding:", err)
				}
				return

			default:
				// conn is closed by handedOff's defer: one deferred here
				// piled up per message from a client retrying an offline
				// device on the same socket.
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
					if err := writeClient(conn, respBin); err != nil {
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
					for attempt := 0; picked == nil && attempt < cPairMaxAttempts; attempt++ {
						// pool.lock is held only long enough to pop a
						// candidate - never across the round trips to the
						// device below, and never across Close(). Close()
						// waits for the relay's readLoop to exit, and that
						// readLoop's last act (failAll -> onDeath ->
						// onDeviceConnectionDied) takes pool.lock itself:
						// closing a candidate that died mid-claim while
						// still holding the lock deadlocked this goroutine
						// against the relay's own, and with it the whole
						// pool - every later registration from that device
						// and every static-asset fetch for it queued behind
						// the lock forever, until the bridge was restarted.
						// That is exactly what took cala off the air on
						// 2026-09-22: its otc process restarted while a
						// client was mid-claim on one of its relays.
						// Holding the lock across forward() was also
						// serialising every client's claim on the same
						// device behind one 90s timeout, for no reason.
						pool.lock.Lock()
						if len(pool.availableConns) == 0 {
							pool.lock.Unlock()
							break
						}
						candidate := pool.availableConns[0]
						pool.availableConns = pool.availableConns[1:]
						pool.lock.Unlock()

						log.Debug("Connecting")
						// Issue #117: tell the device who this relay now
						// serves, before its first request - the device
						// only ever sees the bridge's own address otherwise.
						// Best-effort: an old device that doesn't know the
						// message answers with an error, which is fine.
						if infoFrame, err := proto.Marshal(&pb.ReqEnvelope{
							Id: 0,
							Payload: &pb.ReqEnvelope_ReqBridgeClientInfo{
								ReqBridgeClientInfo: &pb.BridgeClientInfo{RemoteAddr: clientAddr(r, conn)},
							},
						}); err == nil {
							_, releaseInfo, err := candidate.forward(infoFrame)
							releaseInfo()
							if err != nil {
								log.Error("error sending client info to the device:", err)
								candidate.Close()
								continue
							}
						}
						respFrame, releaseResp, err := candidate.forward(frame)
						if err != nil {
							log.Error("Error fordwading message:", err)
							candidate.Close()
							continue
						}
						log.Debug("Connected")

						picked = candidate
						relay = candidate
						conn.SetReadDeadline(time.Time{})
						// Single use connection, close as soon as it is
						// finished since they are authenticated. Close()
						// triggers candidate's onDeath exactly once (via
						// failAll), decrementing liveCount the same way a
						// genuine network failure would - see
						// deviceRelay.onDeath's doc comment.
						defer relay.Close()

						err = writeClient(conn, respFrame)
						releaseResp()
						if err != nil {
							log.Error("error responding, closing the connection:", err)
							return
						}
						if err := mg.dao.RecordDeviceActivity(r.Host, int64(len(frame)), int64(len(respFrame))); err != nil {
							log.Error("error recording device activity:", err)
						}
					}
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
					if err := writeClient(conn, respBin); err != nil {
						log.Error("error responding, closing the connection:", err)
						return
					}
				}
			}
		}
	}
}

// baseHost is the bridge's own host ([otc-api] tld), for the account page
// address in messages to devices.
func (mg *Manager) baseHost() string {
	return cfg.GetStr("otc-api", "tld")
}

// isClientInfo reports whether frame is a ReqEnvelope carrying
// req_bridge_client_info, read from the wire format without decoding the
// rest of the (possibly large) message.
const (
	// cMaxInFlight caps one client's requests relayed at once.
	cMaxInFlight = 32
	// cUnpairedReadLimit: before a client is paired with a device, and for
	// a device's own registration messages - all small.
	cUnpairedReadLimit = 8 << 20
	// cRelayedReadLimit: a relayed request may carry a whole file.
	cRelayedReadLimit = 1000<<20 + 1<<20
)

// frameBudget bounds the memory of large messages being relayed at once.
var frameBudget = wsframe.NewBudget(8 << 30)

// replyBudget is frameBudget for the devices' replies. Separate: a client's
// request holds its frameBudget share until its reply has arrived, so
// replies drawing on the same budget could wait on themselves.
var replyBudget = wsframe.NewBudget(8 << 30)

func isClientInfo(frame []byte) bool {
	const cClientInfoField = 100 // ReqEnvelope.req_bridge_client_info
	b := frame
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		if num == cClientInfoField {
			return true
		}
		b = b[n:]
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return false
		}
		b = b[m:]
	}
	return false
}
