// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"os"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestMemBudgetMakesDownloadsWait(t *testing.T) {
	b := newMemBudget(100)
	first := b.acquire(80)

	done := make(chan struct{})
	released := make(chan struct{})
	go func() {
		release := b.acquire(50) // 80 + 50 > 100: must wait for the first
		close(done)
		release()
		close(released)
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
	<-released
	b.mu.Lock()
	used := b.used
	b.mu.Unlock()
	if used != 0 {
		t.Errorf("budget left at %d, want 0", used)
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

// A file of 2 GiB or more has a wrapped size in its row; the budget goes
// by its blob instead. Anything smaller reserves what the row says.
func TestBudgetSizeSurvivesTheInt32Size(t *testing.T) {
	galleryTestEnv(t)
	big, small := strings.Repeat("7", 64), strings.Repeat("8", 64)
	content := int64(3) << 30
	if err := os.WriteFile(blobPath(big), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(big))
	if err := os.Truncate(blobPath(big), content); err != nil { // sparse
		t.Fatal(err)
	}
	if got := budgetSize(&pb.File{Hash: big, Size: int32(content)}); got != content {
		t.Errorf("a 3 GiB file reserves %d bytes, want %d", got, content)
	}
	if err := os.WriteFile(blobPath(small), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(small))
	if got := budgetSize(&pb.File{Hash: small, Size: 50}); got != 50 {
		t.Errorf("a small file reserves %d, want the row's 50", got)
	}
	if got := budgetSize(&pb.File{Hash: strings.Repeat("9", 64), Size: 70}); got != 70 {
		t.Errorf("a missing blob reserves %d, want the row's 70", got)
	}
}
