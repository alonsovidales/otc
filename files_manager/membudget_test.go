// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"testing"
	"time"
)

func TestMemBudgetMakesDownloadsWait(t *testing.T) {
	b := newMemBudget(100)
	first := b.acquire(80)

	done := make(chan struct{})
	go func() {
		release := b.acquire(50) // 80 + 50 > 100: must wait for the first
		close(done)
		release()
	}()
	select {
	case <-done:
		t.Fatal("a download that doesn't fit went ahead anyway")
	case <-time.After(100 * time.Millisecond):
	}

	first()
	first() // releasing twice must not free the budget twice
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the waiting download never started after the first finished")
	}
	if b.used != 0 {
		t.Errorf("budget left at %d, want 0", b.used)
	}
}

func TestMemBudgetLetsAnOversizedFileRunAlone(t *testing.T) {
	b := newMemBudget(100)
	done := make(chan struct{})
	go func() {
		release := b.acquire(1000) // bigger than the whole budget
		close(done)
		release()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a file bigger than the budget could never be downloaded")
	}
}
