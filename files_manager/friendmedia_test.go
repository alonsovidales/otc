// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"testing"
	"time"
)

// A friend's media waits for room like a download, but only so long: then
// it goes ahead, and the budget is whole again once both are released.
func TestAcquireWithinWaitsBoundedly(t *testing.T) {
	b := newMemBudget(100)
	first := b.acquire(80)

	start := time.Now()
	release := b.acquireWithin(50, 50*time.Millisecond)
	if waited := time.Since(start); waited < 40*time.Millisecond {
		t.Fatalf("went ahead after %v with no room", waited)
	}
	b.mu.Lock()
	if b.used != 130 {
		t.Errorf("used %d, want 130 once the wait ran out", b.used)
	}
	b.mu.Unlock()

	release()
	release() // once only
	first()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used != 0 {
		t.Errorf("budget left at %d, want 0", b.used)
	}
}

// With room it doesn't wait, and a release lets a waiting one in at once.
func TestAcquireWithinTakesRoomAtOnce(t *testing.T) {
	b := newMemBudget(100)
	start := time.Now()
	release := b.acquireWithin(60, time.Minute)
	if time.Since(start) > time.Second {
		t.Fatal("waited with room to spare")
	}
	done := make(chan func())
	go func() { done <- b.acquireWithin(60, time.Minute) }()
	time.Sleep(20 * time.Millisecond)
	release()
	select {
	case r := <-done:
		r()
	case <-time.After(5 * time.Second):
		t.Fatal("a release didn't let the waiting one in")
	}
	if (*Manager)(nil).ReserveFriendMedia(10) == nil {
		t.Fatal("nil release")
	}
}
