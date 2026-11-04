// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"sync/atomic"
	"testing"
	"time"
)

// waitClosed reports whether closed turns true within d.
func waitClosed(closed *atomic.Bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if closed.Load() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return closed.Load()
}

// A connection that stays anonymous closes once idle; one answering a
// request, one signed in, and one done never do.
func TestAnonWatch(t *testing.T) {
	const idle = 60 * time.Millisecond
	watch := func(anonymous bool) (*anonWatch, *atomic.Bool) {
		var closed atomic.Bool
		w := newAnonWatch(idle, func() bool { return anonymous }, func() { closed.Store(true) })
		return w, &closed
	}

	w, closed := watch(true)
	w.arm()
	if !waitClosed(closed, 20*idle) {
		t.Fatal("an idle anonymous connection stayed open")
	}

	// Not armed (a bridge connection waiting in the pool): never.
	w, closed = watch(true)
	if waitClosed(closed, 4*idle) {
		t.Fatal("an unarmed connection was closed")
	}
	w.stop()

	// A request being answered holds it open, however long; the clock
	// starts once it is answered.
	w, closed = watch(true)
	w.arm()
	w.begin()
	if waitClosed(closed, 4*idle) {
		t.Fatal("closed while a request was being answered")
	}
	w.end()
	if !waitClosed(closed, 20*idle) {
		t.Fatal("not closed once idle after its answer")
	}

	// Requests keep it open as they come.
	w, closed = watch(true)
	w.arm()
	for range 6 {
		time.Sleep(idle / 2)
		w.begin()
		w.end()
	}
	if closed.Load() {
		t.Fatal("closed while requests kept coming")
	}
	w.stop()

	// Signed in: never.
	w, closed = watch(false)
	w.arm()
	if waitClosed(closed, 4*idle) {
		t.Fatal("a signed-in connection was closed")
	}

	// Done: never.
	w, closed = watch(true)
	w.arm()
	w.stop()
	if waitClosed(closed, 4*idle) {
		t.Fatal("a stopped watch closed its connection")
	}
}

// One address (an IPv6 /64) holds at most perAddr anonymous sockets, all
// of them total; a release gives its slot back once.
func TestAnonSlots(t *testing.T) {
	s := &anonSlots{perAddr: 2, total: 3}
	r1, ok1 := s.take("192.168.1.5")
	_, ok2 := s.take("::ffff:192.168.1.5")
	if _, ok := s.take("192.168.1.5"); !ok1 || !ok2 || ok {
		t.Fatalf("per address: %v %v %v", ok1, ok2, ok)
	}
	// Two addresses of one /64 are one; another /64 is another.
	if _, ok := s.take("2001:db8:1:2::10"); !ok {
		t.Fatal("a first IPv6 address refused")
	}
	if _, ok := s.take("2001:db8:1:3::10"); ok {
		t.Fatal("over the total")
	}
	r1()
	r1()
	if _, ok := s.take("2001:db8:1:2::11"); !ok {
		t.Fatal("the slot given back isn't there")
	}
	if _, ok := s.take("192.168.1.5"); ok {
		t.Fatal("a release counted twice")
	}
	for addr, want := range map[string]string{
		"192.168.1.5":        "192.168.1.5",
		"::ffff:10.0.0.1":    "10.0.0.1",
		"2001:db8:1:2:3::4":  "2001:db8:1:2::/64",
		"not an address":     "not an address",
		" 2001:db8::1 ":      "2001:db8::/64",
		"fd00:1:2:3:4:5:6:7": "fd00:1:2:3::/64",
	} {
		if got := anonBucket(addr); got != want {
			t.Errorf("%q: %q, want %q", addr, got, want)
		}
	}
}
