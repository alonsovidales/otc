// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
)

// Before pairing a message is read with 64 KiB outside a budget of its
// own; a paired client's with 256 KiB outside the relay's. Both were read
// whole up to 4 MiB, outside any budget.
func TestClientFramesAreBudgetedPastAFewKiB(t *testing.T) {
	if limit, free, b := frameRules(false); limit != cUnpairedReadLimit || free != 64<<10 || b != unpairedBudget {
		t.Errorf("unpaired: limit %d, free %d, budget %p; want %d, 64 KiB, unpairedBudget", limit, free, b, cUnpairedReadLimit)
	}
	if limit, free, b := frameRules(true); limit != cRelayedReadLimit || free != 256<<10 || b != frameBudget {
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
