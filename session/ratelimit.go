// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"sync"
	"time"
)

// Issue #117: a cap on password attempts, per address.
//
// Without one, a wrong password costs an attacker only the one-second delay
// the auth handler already adds, and a device reachable through the bridge
// is reachable by anyone. Five failures in a minute lock that address out
// for a minute, and the refusal says for how long - the point is to make
// guessing hopeless, not to lock the owner out of their own device over a
// typo.
//
// Process-local and never persisted, exactly like the session tokens next
// door: a restart forgets every count, which costs an attacker nothing
// they couldn't get by waiting a minute. Keyed by the address the device
// sees, or the one the bridge reports for a relayed client (see
// BridgeClientInfo in messages.proto) - a client the device can't place at
// all shares one bucket, so being unidentifiable is not a way around it.
const (
	MaxAuthAttempts = 5
	AuthWindow      = time.Minute
	AuthLockout     = time.Minute

	cLimiterSweep = 10 * time.Minute

	// Device-wide, whatever the address: at most MaxAuthFailuresTotal
	// failed passwords per AuthTotalWindow. No person types that fast; a
	// bot spreading its guesses over many addresses is held to ~20 a
	// minute. (It can also hold the owner up while it keeps failing -
	// accepted: it lasts only as long as the flood.)
	MaxAuthFailuresTotal = 10
	AuthTotalWindow      = 30 * time.Second

	// cAuthSlots: password checks at once, the same as deriveSlots,
	// which they would queue on anyway.
	cAuthSlots = 2
)

type attempts struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
}

// AuthLimiter tracks failed password attempts by address.
type AuthLimiter struct {
	mu sync.Mutex
	// failuresAll: when each failure of the last AuthTotalWindow happened,
	// across every address.
	failuresAll []time.Time
	by          map[string]*attempts
	now         func() time.Time
	last        time.Time // last sweep
	// slots: see Attempt.
	slots chan struct{}
}

// Attempts is the process-wide limiter the auth handler consults.
var Attempts = NewAuthLimiter()

func NewAuthLimiter() *AuthLimiter {
	return &AuthLimiter{by: map[string]*attempts{}, now: time.Now, slots: make(chan struct{}, cAuthSlots)}
}

// Attempt runs one password check, try, for addr unless addr may not try
// right now. The limit is checked, and the outcome recorded, while
// holding one of a few slots: requests queued behind a failure see it, so
// a burst of pipelined or parallel guesses is refused once the limit trips
// instead of each running a full Argon2 derivation first. lockedFor is
// the lockout a failure caused, as Fail returns it.
func (l *AuthLimiter) Attempt(addr string, try func() error) (retryAfter time.Duration, blocked bool, lockedFor time.Duration, err error) {
	l.slots <- struct{}{}
	defer func() { <-l.slots }()
	if retryAfter, blocked = l.Blocked(addr); blocked {
		return retryAfter, true, 0, nil
	}
	if err = try(); err != nil {
		return 0, false, l.Fail(addr), err
	}
	l.Reset(addr)
	return 0, false, 0, nil
}

func (l *AuthLimiter) key(addr string) string {
	if addr == "" {
		return "unknown"
	}
	return addr
}

// Blocked reports whether addr may not try right now, and for how long.
func (l *AuthLimiter) Blocked(addr string) (retryAfter time.Duration, blocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked()
	now := l.now()
	if wait, full := l.totalFullLocked(now); full {
		return wait, true
	}
	a := l.by[l.key(addr)]
	if a == nil {
		return 0, false
	}
	if now.Before(a.lockedUntil) {
		return a.lockedUntil.Sub(now), true
	}
	return 0, false
}

// Fail records one wrong password from addr, locking it out once it has
// spent its allowance within the window. Returns the lockout it caused, if
// this was the attempt that did.
func (l *AuthLimiter) Fail(addr string) (lockedFor time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.totalFullLocked(now) // drop failures that left the window
	l.failuresAll = append(l.failuresAll, now)
	k := l.key(addr)
	a := l.by[k]
	if a == nil || now.Sub(a.windowStart) > AuthWindow {
		a = &attempts{windowStart: now}
		l.by[k] = a
	}
	a.failures++
	if a.failures >= MaxAuthAttempts {
		a.lockedUntil = now.Add(AuthLockout)
		a.failures = 0
		a.windowStart = now
		return AuthLockout
	}
	return 0
}

// Reset forgets addr after a successful sign-in.
func (l *AuthLimiter) Reset(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.by, l.key(addr))
}

// totalFullLocked prunes failuresAll to the window and reports whether
// the device-wide limit is reached, and for how long it stays so.
func (l *AuthLimiter) totalFullLocked(now time.Time) (time.Duration, bool) {
	cut := 0
	for cut < len(l.failuresAll) && now.Sub(l.failuresAll[cut]) >= AuthTotalWindow {
		cut++
	}
	l.failuresAll = l.failuresAll[cut:]
	if len(l.failuresAll) < MaxAuthFailuresTotal {
		return 0, false
	}
	oldest := l.failuresAll[len(l.failuresAll)-MaxAuthFailuresTotal]
	return AuthTotalWindow - now.Sub(oldest), true
}

// sweepLocked drops entries that can no longer matter, now and then, so
// the map doesn't grow with every address that ever got a password wrong.
func (l *AuthLimiter) sweepLocked() {
	now := l.now()
	if now.Sub(l.last) < cLimiterSweep {
		return
	}
	l.last = now
	for k, a := range l.by {
		if now.After(a.lockedUntil) && now.Sub(a.windowStart) > AuthWindow {
			delete(l.by, k)
		}
	}
}
