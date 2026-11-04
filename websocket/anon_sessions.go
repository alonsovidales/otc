// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
)

// Sessions that never sign in (nor authenticate as a friend). gorilla's
// Upgrade clears every deadline, and the device never pings, so a socket
// opened to the device's own port that never sent a thing lived for as
// long as its TCP connection did - each a goroutine, its buffers, a
// connHandler and a live entry, ~20-30 KB - and nothing bounded how many
// there were: tens of thousands from one address (a container on a public
// address, Tailscale Funnel, the LAN) could take a Pi's memory. Two rules:
//
//   - anonWatch: a session still anonymous after cAnonIdleDirect (on the
//     device's own listener) or cAnonIdleBridged (a bridge connection,
//     counted from its first relayed message: idle in the pool it waits
//     for a client, by design) with no request read and none being
//     answered is closed. The web app's pages and the shared ones open a
//     new socket at their next request, signing in on it as before; the
//     apps sign in at once.
//   - anonSlots: one address (a global IPv6 one's /64, anonBucket) holds
//     at most cMaxAnonPerAddr
//     sockets to the device's own listener before they sign in - past
//     that a new one is answered 503 before the upgrade - and all of them
//     together cMaxAnonDirect: past that a new one gets in and the longest
//     idle of the address holding the most is closed. A connection stops
//     counting once it signs in. Bridge connections are bounded by the
//     bridge's own caps.
var (
	cAnonIdleDirect  = 2 * time.Minute
	cAnonIdleBridged = 15 * time.Minute
)

const (
	cMaxAnonPerAddr = 256
	cMaxAnonDirect  = 2048
)

// anonWatch closes a connection that stays anonymous and idle for idle:
// no request read and none in flight for that long.
type anonWatch struct {
	idle      time.Duration
	anonymous func() bool
	closeConn func()

	mu       sync.Mutex
	armed    bool
	stopped  bool
	inFlight int
	last     time.Time
	timer    *time.Timer
}

func newAnonWatch(idle time.Duration, anonymous func() bool, closeConn func()) *anonWatch {
	return &anonWatch{idle: idle, anonymous: anonymous, closeConn: closeConn}
}

// arm starts the clock (a direct connection at once, a bridge connection at
// its first relayed message). Arming again does nothing.
func (w *anonWatch) arm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.armed || w.stopped {
		return
	}
	w.armed = true
	w.last = time.Now()
	w.timer = time.AfterFunc(w.idle, w.fire)
}

// begin notes a request read; end, that it has been answered (or dropped).
func (w *anonWatch) begin() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inFlight++
	w.last = time.Now()
}

func (w *anonWatch) end() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inFlight--
	w.last = time.Now()
	if w.inFlight == 0 && w.timer != nil && !w.stopped {
		w.timer.Reset(w.idle)
	}
}

// stop ends the watch: the connection is done.
func (w *anonWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
}

func (w *anonWatch) fire() {
	w.mu.Lock()
	if w.stopped || w.inFlight > 0 {
		// Answering: end() starts the clock again.
		w.mu.Unlock()
		return
	}
	if !w.anonymous() {
		// Signed in, or a friend: never closed here.
		w.stopped = true
		w.mu.Unlock()
		return
	}
	if left := w.idle - time.Since(w.last); left > 0 {
		w.timer.Reset(left)
		w.mu.Unlock()
		return
	}
	w.stopped = true
	w.mu.Unlock()
	log.Debug("closing a connection that stayed idle without signing in")
	anonLog.note(cAnonIdle)
	w.closeConn()
}

// activity is when the connection last read or answered a request, and
// whether one is being answered now.
func (w *anonWatch) activity() (last time.Time, busy bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last, w.inFlight > 0
}

// anonSlots counts the sockets to the device's own listener that haven't
// signed in, by address (anonBucket) and in all. One address past perAddr
// is refused. All of them together past total, the newcomer is let in and
// another goes: the longest idle of the address that holds the most
// (pushOut). Refusing every newcomer once the total was reached let a few
// addresses keeping their sockets busy lock out everyone else - the
// owner's own apps and pages, which open a socket before they sign in -
// while now a new address always gets in, and one that keeps opening
// sockets only pushes out its own.
type anonSlots struct {
	perAddr, total int

	mu     sync.Mutex
	byAddr map[string]map[*anonSlot]struct{}
	all    int
}

var directAnon = &anonSlots{perAddr: cMaxAnonPerAddr, total: cMaxAnonDirect}

// anonSlot is one socket's place in anonSlots, from before its upgrade
// until it signs in or ends. Its methods are nil-safe.
type anonSlot struct {
	s      *anonSlots
	bucket string

	// Under s.mu. closeConn and activity are set once the socket is
	// served (attach): until then it can't be pushed out.
	closeConn func()
	activity  func() (last time.Time, busy bool)
	gone      bool
}

// take takes a slot for a socket from addr. ok false: its address holds
// as many as it may, or every address together does and none can go.
func (s *anonSlots) take(addr string) (slot *anonSlot, ok bool) {
	b := anonBucket(addr)
	s.mu.Lock()
	if s.byAddr == nil {
		s.byAddr = map[string]map[*anonSlot]struct{}{}
	}
	if len(s.byAddr[b]) >= s.perAddr {
		s.mu.Unlock()
		return nil, false
	}
	var pushed func()
	if s.all >= s.total {
		if pushed = s.pushOut(b); pushed == nil {
			s.mu.Unlock()
			return nil, false
		}
	}
	slot = &anonSlot{s: s, bucket: b}
	if s.byAddr[b] == nil {
		s.byAddr[b] = map[*anonSlot]struct{}{}
	}
	s.byAddr[b][slot] = struct{}{}
	s.all++
	s.mu.Unlock()
	if pushed != nil {
		log.Debug("closing a connection that hasn't signed in, to make room for one from", addr)
		anonLog.note(cAnonPushedOut)
		pushed()
	}
	return slot, true
}

// pushOut takes the place of the socket that goes for a newcomer of
// bucket: of the address holding the most - the newcomer's own when it
// holds as many - the one idle longest, one answering a request only when
// that address has no other. Returns what closes it, or nil when no
// socket can go (none served yet). Under s.mu.
func (s *anonSlots) pushOut(bucket string) func() {
	type candidate struct {
		slot      *anonSlot
		held      int
		own, busy bool
		last      time.Time
	}
	// before: a goes before b.
	before := func(a, b candidate) bool {
		switch {
		case a.held != b.held:
			return a.held > b.held
		case a.own != b.own:
			return a.own
		case a.busy != b.busy:
			return !a.busy
		}
		return a.last.Before(b.last)
	}
	var best *candidate
	for b, slots := range s.byAddr {
		if best != nil && len(slots) < best.held {
			continue
		}
		for slot := range slots {
			if slot.closeConn == nil {
				continue
			}
			c := candidate{slot: slot, held: len(slots), own: b == bucket}
			c.last, c.busy = slot.activity()
			if best == nil || before(c, *best) {
				best = &c
			}
		}
	}
	if best == nil {
		return nil
	}
	s.drop(best.slot)
	return best.slot.closeConn
}

// drop takes slot out of the counts, once. Under s.mu.
func (s *anonSlots) drop(slot *anonSlot) {
	if slot.gone {
		return
	}
	slot.gone = true
	if slots := s.byAddr[slot.bucket]; slots != nil {
		delete(slots, slot)
		if len(slots) == 0 {
			delete(s.byAddr, slot.bucket)
		}
	}
	s.all--
}

// attach makes the socket served on slot one that can be pushed out:
// closeConn closes it, activity says how idle it is. A slot pushed out
// already (it can't be before this) or given back does nothing.
func (slot *anonSlot) attach(closeConn func(), activity func() (time.Time, bool)) {
	if slot == nil {
		return
	}
	slot.s.mu.Lock()
	defer slot.s.mu.Unlock()
	if !slot.gone {
		slot.closeConn, slot.activity = closeConn, activity
	}
}

// release gives the slot back: the socket signed in or ended. Once,
// whoever calls it, and nothing for a slot pushed out.
func (slot *anonSlot) release() {
	if slot == nil {
		return
	}
	slot.s.mu.Lock()
	defer slot.s.mu.Unlock()
	slot.s.drop(slot)
}

// anonBucket is what anonSlots and keyTurns count an address under: the
// address, or for a global IPv6 one its /64 - one subscriber's prefix,
// whose addresses cost nothing to change. The home network's own IPv6
// addresses count one by one, as IPv4 ones do: every device of a home
// shares its /64, and one of them filling that bucket would leave every
// other one's new socket a 503, and put their sign-ins behind all of its
// key requests. Those are unique local (fc00::/7, Tailscale's too),
// link-local, loopback, and the device's own networks (onLinkV6).
func anonBucket(addr string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return addr
	}
	a = a.Unmap()
	if a.Is6() && !homeV6(a) {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

// homeV6 reports whether a is an IPv6 address of the home network's own.
func homeV6(a netip.Addr) bool {
	if a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLoopback() {
		return true
	}
	a = a.WithZone("")
	for _, p := range onLinkV6() {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// onLinkV6 is the device's own global IPv6 networks (onLinkNets), read
// again at most once a minute: the prefix a router hands out can change.
// A var for tests.
var onLinkV6 = (&onLinkCache{every: time.Minute, read: interfaceV6Nets}).get

type onLinkCache struct {
	every time.Duration
	read  func() []netip.Prefix

	mu   sync.Mutex
	at   time.Time
	nets []netip.Prefix
}

func (c *onLinkCache) get() []netip.Prefix {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() || time.Since(c.at) >= c.every {
		c.nets, c.at = c.read(), time.Now()
	}
	return c.nets
}

// interfaceV6Nets is onLinkNets of the addresses of this machine's up
// interfaces, loopback left out.
func interfaceV6Nets() []netip.Prefix {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var addrs []net.Addr
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		if a, err := i.Addrs(); err == nil {
			addrs = append(addrs, a...)
		}
	}
	return onLinkNets(addrs)
}

// onLinkNets is the network of each global IPv6 address in addrs, as the
// interface has it, at most a /64 (an address on a wider one stands for
// its own /64): a home's /64, where the router's SLAAC puts every device.
// An address with no network of its own (a /128, as DHCPv6 and some
// servers' providers give out) says nothing about who else is on it.
func onLinkNets(addrs []net.Addr) []netip.Prefix {
	var out []netip.Prefix
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		ones, bits := n.Mask.Size()
		if !ip.Is6() || bits != 128 || ones >= 128 || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		if p, err := ip.Prefix(max(ones, 64)); err == nil {
			out = append(out, p)
		}
	}
	return out
}
