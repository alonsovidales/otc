// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
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
//   - anonSlots: one address (an IPv6 /64) holds at most cMaxAnonPerAddr
//     sockets to the device's own listener before they sign in, and all of
//     them together cMaxAnonDirect; past that a new one is answered 503
//     before the upgrade. A connection stops counting once it signs in.
//     Bridge connections are bounded by the bridge's own caps.
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

// anonSlots counts the sockets to the device's own listener that haven't
// signed in, by address (anonBucket) and in all.
type anonSlots struct {
	perAddr, total int

	mu     sync.Mutex
	byAddr map[string]int
	all    int
}

var directAnon = &anonSlots{perAddr: cMaxAnonPerAddr, total: cMaxAnonDirect}

// take takes a slot for a socket from addr; release gives it back (once,
// whatever calls it; nil-safe to call more than once). ok false: none
// left, for that address or in all.
func (s *anonSlots) take(addr string) (release func(), ok bool) {
	b := anonBucket(addr)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAddr == nil {
		s.byAddr = map[string]int{}
	}
	if s.byAddr[b] >= s.perAddr || s.all >= s.total {
		return func() {}, false
	}
	s.byAddr[b]++
	s.all++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.byAddr[b]--; s.byAddr[b] <= 0 {
				delete(s.byAddr, b)
			}
			s.all--
		})
	}, true
}

// anonBucket is what anonSlots counts an address under: the address, or
// for IPv6 its /64 - one subscriber's prefix, whose addresses cost nothing
// to change.
func anonBucket(addr string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return addr
	}
	a = a.Unmap()
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}
