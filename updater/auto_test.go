// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// autoBed points the state at a scratch file and returns deps whose calls
// are counted.
type autoBed struct {
	now     time.Time
	pending bool
	info    *Info
	err     error
	ran     bool
	applErr error
	checks  int
	applies int
}

func newAutoBed(t *testing.T) *autoBed {
	t.Helper()
	path := filepath.Join(t.TempDir(), autoStateFile)
	old := autoStatePath
	autoStatePath = func() string { return path }
	t.Cleanup(func() { autoStatePath = old })

	return &autoBed{
		now:  time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC),
		info: &Info{CurrentVersion: 183, LatestVersion: 184, Pending: []Release{{Version: 184}}},
		ran:  true,
	}
}

func (b *autoBed) deps() autoDeps {
	return autoDeps{
		now:     func() time.Time { return b.now },
		pending: func() bool { return b.pending },
		check:   func() (*Info, error) { b.checks++; return b.info, b.err },
		apply:   func() (bool, error) { b.applies++; return b.ran, b.applErr },
	}
}

func TestAutomaticUpdatesAreOnUnlessTurnedOff(t *testing.T) {
	newAutoBed(t)
	if !AutoEnabled() {
		t.Fatal("automatic updates are off on a device that never chose")
	}
	if err := SetAuto(false); err != nil {
		t.Fatal(err)
	}
	if AutoEnabled() {
		t.Fatal("still on after being turned off")
	}
	if err := SetAuto(true); err != nil {
		t.Fatal(err)
	}
	if !AutoEnabled() {
		t.Fatal("still off after being turned on")
	}
}

func TestAutoPassInstallsWhatIsNewEveryTwelveHours(t *testing.T) {
	b := newAutoBed(t)
	if wait := autoPass(b.deps()); wait != 0 || b.applies != 1 {
		t.Fatalf("first pass: wait %v, %d updates started, want 0 and 1", wait, b.applies)
	}
	// Not again before twelve hours are up.
	b.now = b.now.Add(cAutoEvery - time.Minute)
	autoPass(b.deps())
	if b.checks != 1 || b.applies != 1 {
		t.Fatalf("before 12 h: %d checks, %d updates, want 1 and 1", b.checks, b.applies)
	}
	b.now = b.now.Add(time.Minute)
	autoPass(b.deps())
	if b.checks != 2 || b.applies != 2 {
		t.Fatalf("at 12 h: %d checks, %d updates, want 2 and 2", b.checks, b.applies)
	}
}

func TestAutoPassDoesNothingWhileOff(t *testing.T) {
	b := newAutoBed(t)
	if err := SetAuto(false); err != nil {
		t.Fatal(err)
	}
	autoPass(b.deps())
	if b.checks != 0 || b.applies != 0 {
		t.Fatalf("off: %d checks, %d updates, want none", b.checks, b.applies)
	}
}

func TestAutoPassUpToDateWaitsTwelveHours(t *testing.T) {
	b := newAutoBed(t)
	b.info = &Info{CurrentVersion: 184, LatestVersion: 184}
	autoPass(b.deps())
	b.now = b.now.Add(time.Hour)
	autoPass(b.deps())
	if b.checks != 1 || b.applies != 0 {
		t.Fatalf("up to date: %d checks, %d updates, want 1 and 0", b.checks, b.applies)
	}
}

func TestAutoPassRetriesACheckThatFailed(t *testing.T) {
	b := newAutoBed(t)
	b.err = errors.New("no network")
	if wait := autoPass(b.deps()); wait != cAutoRetry {
		t.Fatalf("a failed check waits %v, want %v", wait, cAutoRetry)
	}
	b.err = nil
	autoPass(b.deps())
	if b.applies != 1 {
		t.Fatalf("after the check worked: %d updates, want 1", b.applies)
	}
}

func TestAutoPassWaitsForAnUpdateOrAWifiChangeUnderWay(t *testing.T) {
	b := newAutoBed(t)
	b.pending = true
	autoPass(b.deps())
	if b.checks != 0 {
		t.Fatal("checked while an update was under way")
	}
	b.pending, b.ran = false, false
	autoPass(b.deps())
	if b.applies != 1 {
		t.Fatalf("%d updates asked for, want 1", b.applies)
	}
	// Held up: still due at the next look.
	b.ran = true
	autoPass(b.deps())
	if b.applies != 2 {
		t.Fatalf("after the Wi-Fi change: %d updates asked for, want 2", b.applies)
	}
}

func TestAutoPassGivesUpOnAReleaseThatKeepsFailing(t *testing.T) {
	b := newAutoBed(t)
	for range cAutoTries + 2 {
		autoPass(b.deps())
		b.now = b.now.Add(cAutoEvery)
	}
	if b.applies != cAutoTries {
		t.Fatalf("release 184 started %d times, want %d", b.applies, cAutoTries)
	}
	// A newer release is tried again.
	b.info = &Info{CurrentVersion: 183, LatestVersion: 185, Pending: []Release{{Version: 184}, {Version: 185}}}
	autoPass(b.deps())
	if b.applies != cAutoTries+1 {
		t.Fatalf("a newer release wasn't tried: %d updates", b.applies)
	}
}

func TestAutoPassAClockSetBackIsDue(t *testing.T) {
	b := newAutoBed(t)
	b.info = &Info{CurrentVersion: 184, LatestVersion: 184}
	autoPass(b.deps())
	b.now = b.now.Add(-48 * time.Hour)
	autoPass(b.deps())
	if b.checks != 2 {
		t.Fatalf("%d checks, want 2: a record dated after now is due", b.checks)
	}
}

func TestAutoPassUnknownVersionInstallsNothing(t *testing.T) {
	b := newAutoBed(t)
	b.info = &Info{VersionUnknown: true, LatestVersion: 184}
	autoPass(b.deps())
	if b.applies != 0 {
		t.Fatal("an update was started without knowing the installed version")
	}
}
