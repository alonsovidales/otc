// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fakeConn struct{ closed atomic.Int32 }

func (c *fakeConn) Close() error { c.closed.Add(1); return nil }

// A network change closes every idle pool connection, forgets the
// backoff and wakes the retries waiting it out - and twice in a row (the
// wake channel already gone) doesn't panic.
func TestNetworkChangedRestartsThePool(t *testing.T) {
	var p bridgeConnPool
	a, b := &fakeConn{}, &fakeConn{}
	wake := make(chan struct{})
	p.idle = map[io.Closer]struct{}{a: {}, b: {}}
	p.consecutiveFailures = 9
	p.wake = wake
	if n := p.networkChanged(); n != 2 {
		t.Errorf("closed %d", n)
	}
	if a.closed.Load() != 1 || b.closed.Load() != 1 {
		t.Error("an idle connection was left open")
	}
	select {
	case <-wake:
	default:
		t.Error("the waiting retries weren't woken")
	}
	if p.consecutiveFailures != 0 || p.wake != nil {
		t.Errorf("backoff %d, wake %v", p.consecutiveFailures, p.wake)
	}
	p.idle = nil
	if n := p.networkChanged(); n != 0 {
		t.Errorf("closed %d of none", n)
	}
}

// takingConn is an idle connection whose closing stands for another one
// being taken for a relay at that moment (it leaves the idle set).
type takingConn struct {
	fakeConn
	p     *bridgeConnPool
	other io.Closer
}

func (c *takingConn) Close() error {
	delete(c.p.idle, c.other)
	return c.fakeConn.Close()
}

// A connection taken for a relay after the idle list was made is left
// alone: it carries a session that just started.
func TestNetworkChangedSparesOneJustTaken(t *testing.T) {
	var p bridgeConnPool
	a := &takingConn{p: &p}
	b := &takingConn{p: &p, other: a}
	a.other = b
	p.idle = map[io.Closer]struct{}{a: {}, b: {}}
	if n := p.networkChanged(); n != 1 {
		t.Errorf("closed %d, want 1", n)
	}
	if a.closed.Load()+b.closed.Load() != 1 {
		t.Errorf("closed a %d, b %d", a.closed.Load(), b.closed.Load())
	}
}

// The watcher acts on a change only: not on its first look, not while
// nothing moves.
func TestWatchNetworkActsOnAChange(t *testing.T) {
	savedEvery, savedFP := cNetCheckEvery, netFingerprint
	var fp atomic.Value
	fp.Store("eth0 192.168.1.20:8080")
	cNetCheckEvery = 5 * time.Millisecond
	netFingerprint = func() string { return fp.Load().(string) }
	mg := &Manager{}
	c := &fakeConn{}
	mg.bridgePool.mu.Lock()
	mg.bridgePool.idle = map[io.Closer]struct{}{c: {}}
	mg.bridgePool.mu.Unlock()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { mg.watchNetwork(stop); close(done) }()
	t.Cleanup(func() {
		close(stop)
		<-done
		cNetCheckEvery, netFingerprint = savedEvery, savedFP
	})

	time.Sleep(50 * time.Millisecond)
	if c.closed.Load() != 0 {
		t.Fatal("closed with no change")
	}
	fp.Store("wlan0 192.168.1.21:8080")
	deadline := time.Now().Add(2 * time.Second)
	for c.closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.closed.Load() == 0 {
		t.Fatal("a change closed nothing")
	}
}

func TestDefaultRouteIface(t *testing.T) {
	saved := routeFile
	t.Cleanup(func() { routeFile = saved })
	routeFile = filepath.Join(t.TempDir(), "route")
	table := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth1\t00000000\t0101A8C0\t0002\t0\t0\t50\t00000000\t0\t0\t0\n" + // down
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	if err := os.WriteFile(routeFile, []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := defaultRouteIface(); got != "eth0" {
		t.Errorf("got %q", got)
	}
	routeFile = filepath.Join(t.TempDir(), "missing")
	if got := defaultRouteIface(); got != "" {
		t.Errorf("no table: %q", got)
	}
}
