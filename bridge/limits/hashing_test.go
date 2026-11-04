// SPDX-License-Identifier: AGPL-3.0-or-later

package limits

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// No more than cHashSlots hashes run at once, and one that finds every
// slot taken for longer than hashWait is ErrBusy - not a queue without
// end; a slot freed in time lets it through.
func TestHashingIsBoundedPerNode(t *testing.T) {
	old, oldWait := hashSlots, hashWait
	t.Cleanup(func() { hashSlots, hashWait = old, oldWait })
	hashSlots, hashWait = make(chan struct{}, 2), 50*time.Millisecond

	hash, err := HashPassword("a-good-password")
	if err != nil || !strings.HasPrefix(hash, "$2a$12$") {
		t.Fatalf("hash %q, %v", hash, err)
	}
	if ok, err := CheckPassword(hash, "a-good-password"); !ok || err != nil {
		t.Fatalf("the right password: %v %v", ok, err)
	}
	if ok, err := CheckPassword(hash, "a-wrong-one"); ok || err != nil {
		t.Fatalf("a wrong password: %v %v", ok, err)
	}
	if ok, err := CheckPassword("not a hash", "x"); ok || err != nil {
		t.Fatalf("a hash that doesn't parse: %v %v", ok, err)
	}
	if _, err := HashPassword(strings.Repeat("x", 73)); err == nil || err == ErrBusy {
		t.Fatalf("73 bytes: %v", err)
	}

	// Both slots held: busy after the wait.
	hashSlots <- struct{}{}
	hashSlots <- struct{}{}
	start := time.Now()
	if _, err := CheckPassword(hash, "a-good-password"); err != ErrBusy || time.Since(start) < hashWait {
		t.Fatalf("every slot taken: %v after %s", err, time.Since(start))
	}
	if _, err := HashPassword("a-good-password"); err != ErrBusy {
		t.Fatalf("every slot taken, hashing: %v", err)
	}
	// One freed within the wait: it goes through.
	go func() { time.Sleep(10 * time.Millisecond); <-hashSlots }()
	if ok, err := CheckPassword(hash, "a-good-password"); !ok || err != nil {
		t.Fatalf("a slot freed in time: %v %v", ok, err)
	}
	<-hashSlots

	// Many at once: never more than the slots.
	var mu sync.Mutex
	running, most := 0, 0
	var wg sync.WaitGroup
	hashWait = 10 * time.Second
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = withHashSlot(func() {
				mu.Lock()
				running++
				most = max(most, running)
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				running--
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	if most != 2 {
		t.Errorf("%d ran at once, want the 2 slots", most)
	}
	if cHashSlots < 1 {
		t.Errorf("%d slots", cHashSlots)
	}
}
