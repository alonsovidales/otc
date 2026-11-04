// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net"
	"net/netip"
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

// One address (an IPv6 /64) holds at most perAddr anonymous sockets; a
// release gives its slot back once. Past the total, a newcomer that no
// served socket can make room for is refused.
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
	// None of them served yet: none can go.
	if _, ok := s.take("2001:db8:1:3::10"); ok {
		t.Fatal("over the total with nothing to close")
	}
	r1.release()
	r1.release()
	if _, ok := s.take("2001:db8:1:2::11"); !ok {
		t.Fatal("the slot given back isn't there")
	}
	if _, ok := s.take("192.168.1.5"); ok {
		t.Fatal("a release counted twice")
	}
	var none *anonSlot
	none.release()
	none.attach(func() {}, nil)
}

// A global IPv6 address counts as its /64; the home network's own - unique
// local, link-local, loopback and the device's own /64 - one by one, as
// IPv4 ones do: one device at home can't fill a bucket every other one
// shares.
func TestAnonBucket(t *testing.T) {
	saved := onLinkV6
	onLinkV6 = func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("2001:db8:aa:bb::/64")} }
	t.Cleanup(func() { onLinkV6 = saved })
	for addr, want := range map[string]string{
		"192.168.1.5":         "192.168.1.5",
		"::ffff:10.0.0.1":     "10.0.0.1",
		"2001:db8:1:2:3::4":   "2001:db8:1:2::/64",
		"not an address":      "not an address",
		" 2001:db8::1 ":       "2001:db8::/64",
		"fd00:1:2:3:4:5:6:7":  "fd00:1:2:3:4:5:6:7",
		"fd7a:115c:a1e0::9":   "fd7a:115c:a1e0::9",
		"fe80::1%eth0":        "fe80::1%eth0",
		"::1":                 "::1",
		"2001:db8:aa:bb::5":   "2001:db8:aa:bb::5",
		"2001:db8:aa:bc::5":   "2001:db8:aa:bc::/64",
		"::ffff:192.168.1.30": "192.168.1.30",
	} {
		if got := anonBucket(addr); got != want {
			t.Errorf("%q: %q, want %q", addr, got, want)
		}
	}

	// Two devices of the home's /64: one filling its own bucket leaves the
	// other's sockets alone.
	s := &anonSlots{perAddr: 1, total: 10}
	if _, ok := s.take("2001:db8:aa:bb::5"); !ok {
		t.Fatal("refused")
	}
	if _, ok := s.take("2001:db8:aa:bb::6"); !ok {
		t.Fatal("another device at home shares a bucket with the first")
	}
	if _, ok := s.take("2001:db8:1:2::6"); !ok {
		t.Fatal("refused")
	}
	if _, ok := s.take("2001:db8:1:2::7"); ok {
		t.Fatal("a stranger's /64 counted address by address")
	}
}

// The device's own networks are its global IPv6 addresses' as configured,
// at most a /64; an address with no network of its own (a /128) and every
// other kind say nothing.
func TestOnLinkNets(t *testing.T) {
	ipnet := func(s string) net.Addr {
		ip, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	got := onLinkNets([]net.Addr{
		ipnet("2001:db8:aa:bb::5/64"),
		ipnet("2001:db8:cc:dd:1::5/80"),
		ipnet("2001:db8:ee::5/48"),
		ipnet("2001:db8:ff::5/128"),
		ipnet("fd00::5/64"),
		ipnet("fe80::5/64"),
		ipnet("192.168.1.5/24"),
		&net.IPAddr{IP: net.ParseIP("2001:db8:1::1")},
	})
	want := []string{"2001:db8:aa:bb::/64", "2001:db8:cc:dd:1::/80", "2001:db8:ee::/64"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

// served is a slot of s taken for addr and served: last its last request,
// busy whether one is being answered. closed turns true once it is closed.
func served(t *testing.T, s *anonSlots, addr string, last time.Time, busy bool) (*anonSlot, *atomic.Bool) {
	t.Helper()
	slot, ok := s.take(addr)
	if !ok {
		t.Fatalf("%s refused", addr)
	}
	var closed atomic.Bool
	slot.attach(func() { closed.Store(true) }, func() (time.Time, bool) { return last, busy })
	return slot, &closed
}

// Past the total, a newcomer gets in and a socket goes: of the address
// holding the most - the newcomer's own when it holds as many - the one
// idle longest, one answering a request last. A few addresses keeping
// their sockets busy can't keep another address out, and the one pushed
// out never counts twice.
func TestAnonSlotsMakeRoomForANewAddress(t *testing.T) {
	now := time.Now()
	s := &anonSlots{perAddr: 3, total: 5}
	_, a1 := served(t, s, "2001:db8:1::1", now.Add(-time.Minute), true)
	_, a2 := served(t, s, "2001:db8:1::2", now.Add(-time.Second), false)
	_, a3 := served(t, s, "2001:db8:1::3", now.Add(-2*time.Second), false)
	_, b1 := served(t, s, "2001:db8:2::1", now.Add(-30*time.Minute), false)
	b2, b2closed := served(t, s, "2001:db8:2::2", now.Add(-time.Hour), false)

	// The owner's page, from another address: in, and the longest idle of
	// the address holding the most that isn't answering goes - not the
	// busy one, not the other address's idler ones.
	owner, ok := s.take("192.168.1.20")
	if !ok {
		t.Fatal("a new address was locked out")
	}
	if !a3.Load() || a1.Load() || a2.Load() || b1.Load() || b2closed.Load() {
		t.Fatalf("closed: a1 %v a2 %v a3 %v b1 %v b2 %v", a1.Load(), a2.Load(), a3.Load(), b1.Load(), b2closed.Load())
	}
	if s.all != 5 || len(s.byAddr["2001:db8:1::/64"]) != 2 {
		t.Fatalf("counts: %d in all, %v", s.all, s.byAddr)
	}

	// Two addresses holding as many: the newcomer's own goes first.
	owner.attach(func() {}, func() (time.Time, bool) { return now, false })
	if _, ok := s.take("2001:db8:2::9"); !ok {
		t.Fatal("refused")
	}
	if !b2closed.Load() || b1.Load() || a2.Load() {
		t.Fatalf("not its own: a2 %v b1 %v b2 %v", a2.Load(), b1.Load(), b2closed.Load())
	}

	// Its own address full: refused, whatever the total.
	s2 := &anonSlots{perAddr: 1, total: 2}
	served(t, s2, "203.0.113.1", now, false)
	served(t, s2, "203.0.113.2", now, false)
	if _, ok := s2.take("203.0.113.1"); ok {
		t.Fatal("over its address's own cap")
	}

	// Only one answering left: it goes rather than lock out a new address.
	s3 := &anonSlots{perAddr: 2, total: 1}
	_, busy := served(t, s3, "203.0.113.1", now, true)
	if _, ok := s3.take("203.0.113.2"); !ok || !busy.Load() {
		t.Fatalf("a busy one is never pushed out: %v %v", ok, busy.Load())
	}

	// One pushed out, given back later as its connection ends: no change.
	b2.release()
	if s.all != 5 {
		t.Fatalf("a pushed-out slot given back again: %d", s.all)
	}
}
