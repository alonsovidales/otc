// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthLimiterLocksAfterMaxAttemptsAndReleases(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewAuthLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < MaxAuthAttempts-1; i++ {
		if got := l.Fail("1.2.3.4"); got != 0 {
			t.Fatalf("attempt %d locked out early", i+1)
		}
		if _, blocked := l.Blocked("1.2.3.4"); blocked {
			t.Fatalf("blocked after only %d failures", i+1)
		}
	}
	if got := l.Fail("1.2.3.4"); got != AuthLockout {
		t.Fatalf("expected the %dth failure to lock for %v, got %v", MaxAuthAttempts, AuthLockout, got)
	}
	retry, blocked := l.Blocked("1.2.3.4")
	if !blocked || retry != AuthLockout {
		t.Fatalf("expected a %v lockout, got blocked=%v retry=%v", AuthLockout, blocked, retry)
	}
	// Another address is unaffected.
	if _, blocked := l.Blocked("5.6.7.8"); blocked {
		t.Fatal("a different address must not share the lockout")
	}
	now = now.Add(AuthLockout + time.Second)
	if _, blocked := l.Blocked("1.2.3.4"); blocked {
		t.Fatal("still blocked after the lockout elapsed")
	}
}

func TestAuthLimiterWindowAndReset(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewAuthLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < MaxAuthAttempts-1; i++ {
		l.Fail("a")
	}
	// Slow guessing gets nowhere: the window has passed, the count starts over.
	now = now.Add(AuthWindow + time.Second)
	if got := l.Fail("a"); got != 0 {
		t.Fatal("failures outside the window must not count towards a lockout")
	}
	// A successful sign-in clears the slate.
	for i := 0; i < MaxAuthAttempts-1; i++ {
		l.Fail("a")
	}
	l.Reset("a")
	if got := l.Fail("a"); got != 0 {
		t.Fatal("Reset must forget earlier failures")
	}
	// Unidentifiable clients share one bucket rather than escaping the limit.
	for i := 0; i < MaxAuthAttempts; i++ {
		l.Fail("")
	}
	if _, blocked := l.Blocked(""); !blocked {
		t.Fatal("an empty address must still be limited")
	}
}

// Device-wide: 10 failures within 30 s from any mix of addresses stop
// every attempt until the oldest of them leaves the window.
func TestAuthLimiterDeviceWideLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewAuthLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < MaxAuthFailuresTotal; i++ {
		l.Fail(fmt.Sprintf("10.0.0.%d", i)) // a new address each time
		now = now.Add(time.Second)
	}
	if _, blocked := l.Blocked("192.0.2.1"); !blocked {
		t.Fatal("11th attempt from a fresh address was allowed")
	}
	now = now.Add(AuthTotalWindow)
	if _, blocked := l.Blocked("192.0.2.1"); blocked {
		t.Fatal("still blocked after the window passed")
	}
}

// A burst of guesses sent at once (32 pipelined on one connection) used to
// pass the limit check together and each run a full Argon2 derivation
// before the first failure was counted. Through Attempt, at most one guess
// beyond the allowance runs; the rest are refused without trying.
func TestAuthLimiterAttemptBoundsConcurrentGuesses(t *testing.T) {
	l := NewAuthLimiter()
	var tries, refused atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, blocked, _, _ := l.Attempt("1.2.3.4", func() error {
				tries.Add(1)
				time.Sleep(5 * time.Millisecond)
				return errors.New("Invalid session")
			})
			if blocked {
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := tries.Load(); n > MaxAuthAttempts+cAuthSlots-1 {
		t.Errorf("%d guesses were tried, want at most %d", n, MaxAuthAttempts+cAuthSlots-1)
	}
	if tries.Load()+refused.Load() != 32 {
		t.Errorf("tried %d + refused %d, want all 32 answered", tries.Load(), refused.Load())
	}
}

// Sign-ins that succeed are never refused, however many arrive at once,
// and a failure's lockout is reported the way Fail reports it.
func TestAuthLimiterAttemptSuccessAndLockout(t *testing.T) {
	l := NewAuthLimiter()
	var wg sync.WaitGroup
	var refused atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, blocked, _, err := l.Attempt("owner", func() error { return nil }); blocked || err != nil {
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	if refused.Load() != 0 {
		t.Fatalf("%d good sign-ins were refused", refused.Load())
	}

	fail := func() error { return errors.New("Invalid session") }
	for i := 0; i < MaxAuthAttempts-1; i++ {
		if _, _, locked, err := l.Attempt("a", fail); err == nil || locked != 0 {
			t.Fatalf("attempt %d: err=%v locked=%v", i+1, err, locked)
		}
	}
	if _, _, locked, _ := l.Attempt("a", fail); locked != AuthLockout {
		t.Fatalf("the attempt that spends the allowance must report the lockout, got %v", locked)
	}
	ran := false
	retry, blocked, _, _ := l.Attempt("a", func() error { ran = true; return nil })
	if !blocked || ran || retry <= 0 {
		t.Fatalf("locked out: blocked=%v ran=%v retry=%v", blocked, ran, retry)
	}
}
