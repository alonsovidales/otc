// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMemory is memAdmission's world for a test: the memory available,
// a clock that moves only when a waiting job sleeps, and how many polls.
type fakeMemory struct {
	avail atomic.Int64
	mu    sync.Mutex
	at    time.Time
	polls atomic.Int32
}

func newFakeMemory(avail int64) (*fakeMemory, *memAdmission) {
	f := &fakeMemory{at: time.Unix(1_800_000_000, 0)}
	f.avail.Store(avail)
	a := &memAdmission{
		available: f.avail.Load,
		now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.at
		},
		sleep: func(d time.Duration) {
			f.polls.Add(1)
			f.mu.Lock()
			f.at = f.at.Add(d)
			f.mu.Unlock()
			time.Sleep(time.Millisecond)
		},
	}
	return f, a
}

// admitted starts admit in the background and reports whether it got in
// within a moment.
func admitted(t *testing.T, a *memAdmission, stop func() bool) (got chan bool) {
	t.Helper()
	got = make(chan bool, 1)
	go func() { got <- a.admit(stop) }()
	return got
}

func within(got chan bool, d time.Duration) (ok, done bool) {
	select {
	case ok = <-got:
		return ok, true
	case <-time.After(d):
		return false, false
	}
}

// A job with nothing else running always starts, whatever the memory:
// processing gets slower when memory is short, it never stops.
func TestAdmissionLetsTheFirstJobStart(t *testing.T) {
	_, a := newFakeMemory(0)
	if !a.admit(nil) {
		t.Fatal("the first job was refused")
	}
	a.done()
	if !a.admit(nil) {
		t.Fatal("a job after the first ended was refused")
	}
	if a.running != 1 {
		t.Errorf("running %d, want 1", a.running)
	}
}

// A job beside a running one waits while the device has less than
// cJobHeadroom available, and starts once the running one ends.
func TestAdmissionWaitsForMemory(t *testing.T) {
	f, a := newFakeMemory(cJobHeadroom - 1)
	a.admit(nil)
	got := admitted(t, a, nil)
	if _, done := within(got, 50*time.Millisecond); done {
		t.Fatal("a second job started with too little memory")
	}
	if f.polls.Load() == 0 {
		t.Error("the waiting job never looked again")
	}
	a.done()
	if ok, done := within(got, 2*time.Second); !done || !ok {
		t.Fatal("the waiting job didn't start once the first ended")
	}
}

// Memory freed by something else - the screen's browser closed, another
// user's instance stopped - lets the waiting job start too.
func TestAdmissionStartsWhenMemoryFrees(t *testing.T) {
	f, a := newFakeMemory(cJobHeadroom / 2)
	a.admit(nil)
	got := admitted(t, a, nil)
	if _, done := within(got, 50*time.Millisecond); done {
		t.Fatal("a second job started with too little memory")
	}
	f.avail.Store(4 * cJobHeadroom)
	if ok, done := within(got, 2*time.Second); !done || !ok {
		t.Fatal("the waiting job didn't start once memory was there")
	}
	if a.running != 2 {
		t.Errorf("running %d, want 2", a.running)
	}
}

// The jobs let in during the last cAdmitSettle count as taking
// cJobHeadroom each, as their memory doesn't show yet: with room for two
// jobs, a burst lets one in beside the first, and the next waits until
// those two have settled.
func TestAdmissionCountsJobsStillSettling(t *testing.T) {
	f, a := newFakeMemory(2*cJobHeadroom + cJobHeadroom/2)
	for i := 0; i < 2; i++ {
		got := admitted(t, a, nil)
		if ok, done := within(got, time.Second); !done || !ok {
			t.Fatalf("job %d of the burst didn't start", i+1)
		}
	}
	if f.polls.Load() != 0 {
		t.Fatalf("the burst's first two waited (%d polls)", f.polls.Load())
	}
	got := admitted(t, a, nil)
	ok, done := within(got, 2*time.Second)
	if !done || !ok {
		t.Fatal("the third job never started")
	}
	if f.polls.Load() < int32(cAdmitSettle/cAdmitPoll) {
		t.Errorf("the third job started after %d polls, before the others settled", f.polls.Load())
	}
}

// Memory that can't be read (off Linux) changes nothing: jobs start as
// they did before.
func TestAdmissionWithoutAReading(t *testing.T) {
	f, a := newFakeMemory(-1)
	for i := 0; i < 6; i++ {
		if !a.admit(nil) {
			t.Fatal("refused")
		}
	}
	if f.polls.Load() != 0 {
		t.Errorf("waited %d polls without a reading", f.polls.Load())
	}
}

// A job called off while it waits (a Reprocess's Cancel) is not counted.
func TestAdmissionCanBeCalledOff(t *testing.T) {
	f, a := newFakeMemory(0)
	a.admit(nil)
	ok := a.admit(func() bool { return f.polls.Load() >= 3 })
	if ok {
		t.Fatal("a job called off was let in")
	}
	if a.running != 1 {
		t.Errorf("running %d, want the first job's 1", a.running)
	}
}

// Every processing job goes through it, on a device with no guard too,
// and its done gives the turn back once only.
func TestBeginProcessingWaitsForMemory(t *testing.T) {
	_, a := newFakeMemory(0)
	mg := &Manager{admission: memAdmission{available: a.available, now: a.now, sleep: a.sleep}}
	first := mg.beginProcessing("a", jobAnalysis)
	started, ended := make(chan struct{}), make(chan struct{})
	go func() {
		done := mg.beginProcessing("b", jobThumbnail)
		close(started)
		done()
		close(ended)
	}()
	select {
	case <-started:
		t.Fatal("a thumbnail started beside an analysis with no memory left")
	case <-time.After(50 * time.Millisecond):
	}
	first()
	first() // idempotent
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the thumbnail never started")
	}
	mg.admission.mu.Lock()
	defer mg.admission.mu.Unlock()
	if mg.admission.running != 0 {
		t.Errorf("running %d after both ended", mg.admission.running)
	}
}
