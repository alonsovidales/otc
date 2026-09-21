// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
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
