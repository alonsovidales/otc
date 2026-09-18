// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Issue #99: these drive the limiter with an explicit clock rather than
// sleeping, so the real 15-minute window/lockout can be exercised without
// the test taking 15 minutes.

func TestLimiterAllowsUntilTheFailureThresholdIsReached(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	for i := 0; i < cLoginMaxFailures-1; i++ {
		if ok, _ := l.allow("10.0.0.1", now); !ok {
			t.Fatalf("locked out after only %d failures, want at least %d", i, cLoginMaxFailures)
		}
		l.recordFailure("10.0.0.1", now)
		now = now.Add(time.Second)
	}

	if ok, _ := l.allow("10.0.0.1", now); !ok {
		t.Error("expected the attempt just below the threshold to still be allowed")
	}
}

func TestLimiterLocksOutAfterTooManyFailures(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	for i := 0; i < cLoginMaxFailures; i++ {
		l.recordFailure("10.0.0.1", now)
		now = now.Add(time.Second)
	}

	ok, retryAfter := l.allow("10.0.0.1", now)
	if ok {
		t.Fatal("expected a lockout once the failure threshold was reached")
	}
	if retryAfter <= 0 || retryAfter > cLoginLockout {
		t.Errorf("retryAfter = %s, want something in (0, %s]", retryAfter, cLoginLockout)
	}
}

func TestLimiterReleasesTheLockoutOnceItElapses(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	for i := 0; i < cLoginMaxFailures; i++ {
		l.recordFailure("10.0.0.1", now)
	}
	if ok, _ := l.allow("10.0.0.1", now); ok {
		t.Fatal("expected to be locked out immediately after the threshold")
	}

	if ok, _ := l.allow("10.0.0.1", now.Add(cLoginLockout+time.Second)); !ok {
		t.Error("expected the lockout to have lapsed")
	}
}

// One IP's lockout must never spill onto anyone else - otherwise a guesser
// could lock the real operator out of their own panel.
func TestLimiterIsPerIP(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	for i := 0; i < cLoginMaxFailures; i++ {
		l.recordFailure("10.0.0.1", now)
	}

	if ok, _ := l.allow("10.0.0.1", now); ok {
		t.Error("expected the offending IP to be locked out")
	}
	if ok, _ := l.allow("10.0.0.2", now); !ok {
		t.Error("expected an unrelated IP to be unaffected")
	}
}

// Failures spread far enough apart are someone forgetting their password
// occasionally, not an attack, and must not accumulate into a lockout.
func TestLimiterForgetsFailuresOlderThanTheWindow(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	for i := 0; i < cLoginMaxFailures*3; i++ {
		l.recordFailure("10.0.0.1", now)
		if ok, _ := l.allow("10.0.0.1", now); !ok {
			t.Fatalf("locked out at spread-out failure %d, window should have reset", i)
		}
		now = now.Add(cLoginWindow + time.Minute)
	}
}

func TestLimiterSuccessClearsEarlierFailures(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	for i := 0; i < cLoginMaxFailures-1; i++ {
		l.recordFailure("10.0.0.1", now)
	}
	l.recordSuccess("10.0.0.1")

	// Starting over: the pre-success failures are gone, so it takes a full
	// fresh threshold to lock out again.
	for i := 0; i < cLoginMaxFailures-1; i++ {
		l.recordFailure("10.0.0.1", now)
	}
	if ok, _ := l.allow("10.0.0.1", now); !ok {
		t.Error("expected failures from before a successful login to have been cleared")
	}
}

func TestLimiterPurgesIdleEntries(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	l.recordFailure("10.0.0.1", now)
	// Any later call sweeps entries nothing has touched for cLimiterEntryTTL.
	l.recordFailure("10.0.0.2", now.Add(cLimiterEntryTTL+time.Minute))

	l.mu.Lock()
	_, stillThere := l.byIP["10.0.0.1"]
	l.mu.Unlock()
	if stillThere {
		t.Error("expected the idle entry to have been purged")
	}
}

// The bridge terminates TLS itself with no proxy in front of it, so
// X-Forwarded-For is attacker-controlled - honouring it would give a
// guesser a fresh bucket per request.
func TestClientIPIgnoresForwardedHeadersAndStripsThePort(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/admin/api/login", nil)
	r.RemoteAddr = "203.0.113.7:54321"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	r.Header.Set("X-Real-IP", "10.0.0.2")

	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP() = %q, want %q", got, "203.0.113.7")
	}
}

// Different source ports from the same host are the same attacker.
func TestClientIPBucketsAllPortsFromOneHostTogether(t *testing.T) {
	first := httptest.NewRequest(http.MethodPost, "/admin/api/login", nil)
	first.RemoteAddr = "203.0.113.7:1111"
	second := httptest.NewRequest(http.MethodPost, "/admin/api/login", nil)
	second.RemoteAddr = "203.0.113.7:2222"

	if clientIP(first) != clientIP(second) {
		t.Error("expected two ports from one host to share a bucket")
	}
}
