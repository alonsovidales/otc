// SPDX-License-Identifier: AGPL-3.0-or-later

package supervisor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A child that keeps stopping is started again less and less often - 4 s,
// doubling, at most 10 minutes - and the owner hears about it once, at the
// alertAfter-th stop in a row; a run of 10 minutes or more starts a new
// series.
func TestRespawnBackoff(t *testing.T) {
	var b respawnBackoff
	var waits []time.Duration
	var alerts []int
	for i := 0; i < 12; i++ {
		w, alert := b.next(time.Second)
		waits = append(waits, w)
		if alert {
			alerts = append(alerts, b.failures)
		}
	}
	want := []time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 64 * time.Second,
		128 * time.Second, 256 * time.Second, 512 * time.Second, maxRespawnDelay, maxRespawnDelay, maxRespawnDelay, maxRespawnDelay}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("pauses %v, want %v", waits, want)
		}
	}
	if len(alerts) != 1 || alerts[0] != alertAfter {
		t.Fatalf("alerted at %v, want once at %d", alerts, alertAfter)
	}
	// Up for 10 minutes: the next stop is the first of a new series, and a
	// new series is alerted again.
	if w, alert := b.next(stableUptime); w != respawnDelay || alert || b.failures != 1 {
		t.Fatalf("after a stable run: %v, %v, %d", w, alert, b.failures)
	}
	for i := 0; i < alertAfter-2; i++ {
		b.next(0)
	}
	if _, alert := b.next(0); !alert {
		t.Fatal("a new series wasn't alerted")
	}
}

// The user's config is written only when it changes: it is rendered at
// every start, on the SD card.
func TestWriteIfChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otc_dev_kim.ini")
	if wrote, err := writeIfChanged(path, []byte("[otc]\n"), 0o640); err != nil || !wrote {
		t.Fatalf("first write: %v, %v", wrote, err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	if wrote, err := writeIfChanged(path, []byte("[otc]\n"), 0o640); err != nil || wrote {
		t.Fatalf("the same content was written again: %v, %v", wrote, err)
	}
	if info, _ := os.Stat(path); !info.ModTime().Equal(old) {
		t.Fatal("the file was touched")
	}
	if wrote, err := writeIfChanged(path, []byte("[otc]\nport=8081\n"), 0o640); err != nil || !wrote {
		t.Fatalf("a change wasn't written: %v, %v", wrote, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "[otc]\nport=8081\n" {
		t.Fatalf("content %q", b)
	}
}

// Stop ends a pause between two starts at once: a user deleted while its
// instance waits out a long pause is let go, not waited for.
func TestStopEndsAPause(t *testing.T) {
	m := &managed{exited: make(chan struct{}), wake: make(chan struct{})}
	s := &Supervisor{procs: map[string]*managed{"u": m}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-time.After(time.Minute):
		case <-m.wake:
		}
		s.forget("u")
	}()
	start := time.Now()
	if err := s.Stop("u"); err != nil {
		t.Fatal(err)
	}
	<-done
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Stop took %v", took)
	}
}
