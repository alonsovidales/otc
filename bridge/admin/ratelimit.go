// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"net/http"
	"sync"
	"time"

	"github.com/alonsovidales/otc/bridge/clientaddr"
)

// Issue #99: the admin login is the single place on the bridge where
// guessing one secret gets someone full operator access - the device list,
// every domain's metrics, the ability to register or delete devices. Until
// now the only thing slowing a guesser down was bcrypt's own cost (see
// checkPassword), which is real but nowhere near enough on its own: it
// still leaves an attacker free to run attempts continuously, forever,
// with nothing ever noticing or stopping them.
//
// This is a per-IP failure counter with a lockout, kept in memory. It's
// deliberately not persisted: like the device session tokens in
// session/tokens.go, a restart clearing it is acceptable (an attacker has
// no way to force one), and it keeps this to a single process-local map
// rather than a DB write on every login attempt - which would itself be a
// way to make the bridge do work on an attacker's behalf.
const (
	// cLoginMaxFailures failed attempts from one IP within cLoginWindow
	// trigger a cLoginLockout pause for that IP. Sized for a human who
	// genuinely forgot which password they used, not for convenience: an
	// operator logs in rarely, so even hitting this by accident costs one
	// coffee break, while it caps a guesser at 5 attempts per 15 minutes.
	cLoginMaxFailures = 5
	cLoginWindow      = 15 * time.Minute
	cLoginLockout     = 15 * time.Minute

	// Entries idle for longer than this are dropped on the next sweep, so
	// a long spray across many source addresses can't grow the map without
	// bound.
	cLimiterEntryTTL = time.Hour
)

type attemptRecord struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
	lastSeen    time.Time
}

type loginLimiter struct {
	mu   sync.Mutex
	byIP map[string]*attemptRecord
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{byIP: map[string]*attemptRecord{}}
}

// clientIP is the limiter's bucket key - see clientaddr.Of for why this
// is the remote address with its port stripped, and never a forwarded-for
// header a guesser could just make up per request.
func clientIP(r *http.Request) string {
	return clientaddr.Of(r)
}

// allow reports whether ip may attempt a login right now, and if not, how
// long it has to wait.
func (l *loginLimiter) allow(ip string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.purgeLocked(now)

	rec, found := l.byIP[ip]
	if !found {
		return true, 0
	}
	rec.lastSeen = now
	if now.Before(rec.lockedUntil) {
		return false, rec.lockedUntil.Sub(now)
	}
	return true, 0
}

// recordFailure counts one failed attempt for ip, starting a lockout once
// cLoginMaxFailures land inside a single cLoginWindow.
func (l *loginLimiter) recordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.purgeLocked(now)

	rec, found := l.byIP[ip]
	if !found {
		rec = &attemptRecord{windowStart: now}
		l.byIP[ip] = rec
	}
	rec.lastSeen = now
	// A window that has fully elapsed since the first failure in it starts
	// over, so occasional typos months apart never accumulate into a
	// lockout.
	if now.Sub(rec.windowStart) > cLoginWindow {
		rec.failures = 0
		rec.windowStart = now
	}
	rec.failures++
	if rec.failures >= cLoginMaxFailures {
		rec.lockedUntil = now.Add(cLoginLockout)
		// Fresh window after the lockout: coming back and failing once
		// more shouldn't immediately re-lock on the previous tally.
		rec.failures = 0
		rec.windowStart = now.Add(cLoginLockout)
	}
}

// recordSuccess clears ip's history - whoever just proved they hold the
// password shouldn't be carrying a partial lockout around from earlier
// fat-fingering it.
func (l *loginLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

// purgeLocked drops entries nothing has touched for cLimiterEntryTTL.
// Callers must hold l.mu.
func (l *loginLimiter) purgeLocked(now time.Time) {
	for ip, rec := range l.byIP {
		if now.Sub(rec.lastSeen) > cLimiterEntryTTL && now.After(rec.lockedUntil) {
			delete(l.byIP, ip)
		}
	}
}
