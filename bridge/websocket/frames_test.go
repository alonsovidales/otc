// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// Before pairing a message is read with 64 KiB outside a budget of its
// own; a paired client's with 256 KiB outside the relay's. Both were read
// whole up to 4 MiB, outside any budget.
func TestClientFramesAreBudgetedPastAFewKiB(t *testing.T) {
	share := wsframe.NewShare(unpairedBudget, cUnpairedShare)
	if limit, free, b := frameRules(false, share); limit != cUnpairedReadLimit || free != 64<<10 || b != share {
		t.Errorf("unpaired: limit %d, free %d, budget %p; want %d, 64 KiB, the address's share", limit, free, b, cUnpairedReadLimit)
	}
	if limit, free, b := frameRules(true, share); limit != cRelayedReadLimit || free != 256<<10 || b != frameBudget {
		t.Errorf("paired: limit %d, free %d, budget %p; want %d, 256 KiB, frameBudget", limit, free, b, cRelayedReadLimit)
	}
	// What one paired socket holds outside the budget at most: was 32
	// messages of 4 MiB, 128 MiB.
	if most := int64(cMaxInFlight) * cRelayedFree; most > 8<<20 {
		t.Errorf("a paired socket may hold %d MiB outside the budget", most>>20)
	}
}

// One address may have only so many sockets open before they pair; one
// over is closed at once, and the address gets its room back as they go.
func TestUnpairedSocketsPerAddressAreCapped(t *testing.T) {
	setVar(t, &cMaxUnpairedPerAddr, 2)
	mg := &Manager{bridges: map[string]*bridgePool{}}
	_, host := newTestBridge(t, mg)
	dialFrom := func(addr string) *gorilla.Conn {
		t.Helper()
		c, _, err := gorilla.DefaultDialer.Dial("ws://"+host, http.Header{"X-Forwarded-For": {addr}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	first, _ := dialFrom("198.51.100.7"), dialFrom("198.51.100.7")
	waitFor(t, func() bool {
		mg.unpairedMu.Lock()
		defer mg.unpairedMu.Unlock()
		return mg.unpairedByAddr["198.51.100.7"] == 2
	}, "two sockets counted")
	if !closedSoon(dialFrom("198.51.100.7"), 2*time.Second) {
		t.Fatal("a third socket not paired yet from one address stayed open")
	}
	if closedSoon(dialFrom("203.0.113.9"), 300*time.Millisecond) {
		t.Fatal("another address was refused")
	}
	first.Close()
	waitFor(t, func() bool {
		mg.unpairedMu.Lock()
		defer mg.unpairedMu.Unlock()
		return mg.unpairedByAddr["198.51.100.7"] == 1
	}, "the closed socket's room given back")
	if closedSoon(dialFrom("198.51.100.7"), 300*time.Millisecond) {
		t.Fatal("the address never got its room back")
	}
}

// The start of a large first message, left unfinished, is held in the
// unpaired sockets' own budget - past 64 KiB, never the relay's - and given
// back when the socket goes.
func TestAStalledFirstMessageHoldsOnlyTheUnpairedBudget(t *testing.T) {
	mg := &Manager{bridges: map[string]*bridgePool{}}
	dial, _ := newTestBridge(t, mg)
	relayHeld := frameBudget.Held()
	c := dial()
	w, err := c.NextWriter(gorilla.BinaryMessage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 1<<20)); err != nil { // of an 8 MiB message
		t.Fatal(err)
	}
	waitFor(t, func() bool { return unpairedBudget.Held() >= (1<<20)-cUnpairedFree }, "the unfinished message budgeted")
	if frameBudget.Held() != relayHeld {
		t.Errorf("an unpaired message took %d bytes of the relay's budget", frameBudget.Held()-relayHeld)
	}
	c.Close()
	waitFor(t, func() bool { return unpairedBudget.Held() == 0 }, "the budget given back")
}

// One address's unfinished first messages hold its share of the unpaired
// budget, never all of it: another address's message past 64 KiB - a
// device's logs, a friend request with a picture - still gets room. It
// waited 30 s and failed, its socket closed.
func TestOneAddressCantTakeTheWholeUnpairedBudget(t *testing.T) {
	setVar(t, &unpairedBudget, wsframe.NewBudget(6<<20))
	setVar(t, &cUnpairedShare, 2<<20)
	setVar(t, &wsframe.RoomWait, 2*time.Second) // past the stranger's share, its sockets wait this long

	mg := &Manager{bridges: map[string]*bridgePool{}}
	_, host := newTestBridge(t, mg)
	dialFrom := func(addr string) *gorilla.Conn {
		t.Helper()
		c, _, err := gorilla.DefaultDialer.Dial("ws://"+host, http.Header{"X-Forwarded-For": {addr}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	for i := 0; i < 5; i++ {
		// 1.5 MiB of a message never finished; once the bridge stops
		// reading, the write waits.
		w, err := dialFrom("198.51.100.7").NextWriter(gorilla.BinaryMessage)
		if err != nil {
			t.Fatal(err)
		}
		go w.Write(make([]byte, 3<<19))
	}
	waitFor(t, func() bool { return unpairedBudget.Held() >= cUnpairedShare-cUnpairedFree }, "the stranger's share taken")
	time.Sleep(100 * time.Millisecond)
	if held := unpairedBudget.Held(); held > cUnpairedShare {
		t.Fatalf("one address holds %d bytes of the unpaired budget, past its share of %d", held, cUnpairedShare)
	}

	frame, err := proto.Marshal(&pb.ReqEnvelope{Id: 5, Lang: strings.Repeat("x", 1<<20)})
	if err != nil {
		t.Fatal(err)
	}
	c := dialFrom("203.0.113.9")
	if err := c.WriteMessage(gorilla.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("another address's 1 MiB message got no answer: %v", err)
	}
}

// An address's sockets read under one share, a /64's within its /48's, as
// long as one of them is open.
func TestUnpairedSharesAreKeptPerAddress(t *testing.T) {
	mg := &Manager{}
	k1, a1, _ := mg.reserveUnpaired("198.51.100.7")
	k2, a2, _ := mg.reserveUnpaired("198.51.100.7")
	k3, b, _ := mg.reserveUnpaired("203.0.113.9")
	if a1 != a2 || a1 == b || a1 == unpairedBudget {
		t.Fatalf("shares %p %p %p: want one for the address, another for the other", a1, a2, b)
	}
	k4, v1, _ := mg.reserveUnpaired("2001:db8:7:1::1")
	k5, v2, _ := mg.reserveUnpaired("2001:db8:7:2::1")
	if v1 == v2 || len(k4) != 2 || mg.unpairedShares[k4[1]] == nil || mg.unpairedShares[k4[1]] != mg.unpairedShares[k5[1]] {
		t.Fatalf("two /64s of one /48: %p %p, keys %v %v", v1, v2, k4, k5)
	}
	for _, k := range [][]string{k1, k2, k3, k4, k5} {
		mg.releaseUnpaired(k)
	}
	if len(mg.unpairedShares) != 0 || len(mg.unpairedByAddr) != 0 {
		t.Errorf("left after every socket went: %v, %v", mg.unpairedShares, mg.unpairedByAddr)
	}
}
