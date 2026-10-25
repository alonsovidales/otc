// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/log"
)

// The bridge pool after a network change. The pool's idle connections
// have no read deadline (a relayed client may stay idle as long as it
// likes), so when the route they used goes - a cable unplugged, the Wi-Fi
// changed from Settings - they sit dead until TCP keepalive notices,
// about 2.5 minutes, while the device counts them available and opens no
// others; and a device that was offline for long waits out a retry
// backoff of up to 5 minutes. Settings > Wi-Fi's own way back ("connect
// it with a cable") depends on the device coming back quickly, so this
// watches for the change itself and starts again at once.

// cNetCheckEvery is how often the route is looked at.
var cNetCheckEvery = 5 * time.Second

// netFingerprint is what a change of network changes: the interface of
// the IPv4 default route with the lowest metric, and the address the
// device reaches the internet from. Seams for the tests.
var (
	routeFile      = "/proc/net/route"
	netFingerprint = func() string { return defaultRouteIface() + " " + localAddress() }
)

// watchNetwork runs for the life of the process on an instance with a
// bridge (each per-user instance runs its own pool, and its own watcher),
// or until stop is closed (the tests; nil never is).
func (mg *Manager) watchNetwork(stop <-chan struct{}) {
	prev := netFingerprint()
	for {
		select {
		case <-stop:
			return
		case <-time.After(cNetCheckEvery):
		}
		fp := netFingerprint()
		if fp == prev {
			continue
		}
		prev = fp
		if n := mg.bridgePool.networkChanged(); n > 0 {
			log.Info("network changed: reconnecting to the bridge (", n, "idle connections closed)")
		} else {
			log.Info("network changed: reconnecting to the bridge")
		}
	}
}

// networkChanged closes every idle connection (each one's own goroutine
// then counts it gone and the pool refills), forgets the retry backoff
// and wakes every retry waiting it out. It answers how many it closed.
func (p *bridgeConnPool) networkChanged() int {
	p.mu.Lock()
	idle := make([]io.Closer, 0, len(p.idle))
	for c := range p.idle {
		idle = append(idle, c)
	}
	p.consecutiveFailures = 0
	// nil once a registration has woken the last waiters: closing it
	// again would panic.
	if p.wake != nil {
		close(p.wake)
		p.wake = nil
	}
	p.mu.Unlock()
	closed := 0
	for _, c := range idle {
		// Still idle, checked right before it goes: one taken for a relay
		// since the list was made carries a session that just started.
		p.mu.Lock()
		if _, still := p.idle[c]; still {
			_ = c.Close()
			closed++
		}
		p.mu.Unlock()
	}
	return closed
}

// defaultRouteIface is the interface of the IPv4 default route with the
// lowest metric ("" for none): /proc/net/route's Iface Destination Gateway
// Flags RefCnt Use Metric Mask, the default route being destination and
// mask 0, up (flag 0x1).
func defaultRouteIface() string {
	raw, err := os.ReadFile(routeFile)
	if err != nil {
		return ""
	}
	best, bestMetric := "", uint64(0)
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[0] == "Iface" || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&1 == 0 {
			continue
		}
		metric, err := strconv.ParseUint(f[6], 10, 64)
		if err != nil {
			continue
		}
		if best == "" || metric < bestMetric {
			best, bestMetric = f[0], metric
		}
	}
	return best
}
