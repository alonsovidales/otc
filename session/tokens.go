// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// Issue #101: a browser has no Keychain/vault of its own to replay a real
// password from on reload the way the native apps do (see App.tsx's mobile
// vs. browser auto-auth branches), so it used to keep the actual account
// password sitting in plain localStorage instead - anything that could
// read that origin's storage (an XSS, a shared/synced browser profile, a
// forensic grab of the profile directory) got the real password, not just
// a way back into one browser tab's session.
//
// This in-memory store lets a client hold a random opaque token instead:
// it's bound to an already-derived *Session (the one Auth already built
// from the real password - see New's Argon2id/vault-secret work), it's
// useless for anything but redeeming back into that same Session, and it
// stops meaning anything the moment this process restarts, unlike the
// password. It's deliberately process-local rather than persisted
// anywhere: exactly like the *Session objects it points at, which have
// never survived a restart either.
//
// Bounded (the demo device's password is public, so anyone can sign in
// and ask for tokens): at most cMaxTokensPerSession per sign-in and
// cMaxTokens in all, the oldest going first past either; expired ones are
// swept every cTokenPurgeEvery. A password change (PasswordChanged) ends
// every one of them.
const (
	// TokenTTL matches the web client's previous cPersistedKeyTTLMs, so
	// this change doesn't shorten how long a browser tab stays signed in.
	TokenTTL = time.Hour

	cTokenBytes = 32

	// cMaxTokensPerSession: one sign-in's tokens at once - a tab rotating
	// its own, a few tabs of one browser sharing it.
	cMaxTokensPerSession = 32
	// cMaxTokens: every session's together.
	cMaxTokens = 10000
	// cTokenPurgeEvery: how often expired tokens are swept out.
	cTokenPurgeEvery = time.Minute
)

// ErrStaleSession: the password changed since the session asking for a
// token was signed in (PasswordChanged).
var ErrStaleSession = errors.New("the password was changed since this session signed in") // i18n-ignore: answered as dev.auth.token_failed

type tokenEntry struct {
	session   *Session
	expiresAt time.Time
	// gen: the password generation the session was signed in under.
	gen uint64
}

var (
	tokenMu sync.Mutex
	tokens  = map[string]*tokenEntry{}
	// issued: every token in the order it was issued, ones no longer in
	// tokens left to drop as they come to the front; all share TokenTTL,
	// so this is also the order they expire in.
	issued []string
	// bySession: each session's tokens in the order issued (the same
	// leftovers).
	bySession = map[*Session][]string{}
	// passwordGen counts this process's password changes (under tokenMu).
	passwordGen uint64

	purgeOnce sync.Once
)

func randomToken() (string, error) {
	b := make([]byte, cTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PasswordGeneration is how many times the password was changed in this
// process: what a connection signs in under. Taken before the password is
// checked, it says whether a change came while it was (PasswordChanged).
func PasswordGeneration() uint64 {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	return passwordGen
}

// PasswordChanged starts a new password generation: every token is gone
// (issued with the old password, whoever holds them), and none is issued
// or redeemed for a sign-in from before. The connection that changed it
// carries on in the generation returned; the caller ends the others.
func PasswordChanged() uint64 {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	passwordGen++
	tokens = map[string]*tokenEntry{}
	issued = nil
	bySession = map[*Session][]string{}
	return passwordGen
}

// IssueToken mints a fresh opaque token bound to ses, valid for TokenTTL.
// gen is the password generation ses's connection signed in under: a
// sign-in from before a password change gets none (ErrStaleSession).
// Called both right after a password-based Auth and every time a client
// redeems a still-valid token (RedeemToken below) - each redemption gets a
// brand new token with a fresh TTL rather than counting down from the
// original login, so a browser tab that's reloaded regularly never has to
// fall back to the password prompt, matching what the old
// savePersistedKey(key)-on-every-successful-auth behavior already did.
func IssueToken(ses *Session, gen uint64) (token string, expiresAt time.Time, err error) {
	purgeOnce.Do(func() { go purgeEvery(cTokenPurgeEvery) })
	token, err = randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	expiresAt = now.Add(TokenTTL)

	tokenMu.Lock()
	defer tokenMu.Unlock()
	if gen != passwordGen {
		return "", time.Time{}, ErrStaleSession
	}
	dropExpiredLocked(now)
	for len(tokens) >= cMaxTokens && dropOldestLocked() {
	}
	mine := liveLocked(bySession[ses])
	for len(mine) >= cMaxTokensPerSession {
		delete(tokens, mine[0])
		mine = mine[1:]
	}
	tokens[token] = &tokenEntry{session: ses, expiresAt: expiresAt, gen: gen}
	bySession[ses] = append(mine, token)
	issued = append(issued, token)
	compactIssuedLocked()
	return token, expiresAt, nil
}

// RedeemToken looks up token and, if it's still valid, returns the Session
// it was issued for and the password generation it was signed in under.
// Single-use: a successful redemption removes it immediately, so a client
// must call IssueToken again for whatever it persists next (see
// ReqAuthWithToken's handler) - this keeps the window in which a leaked
// token is still worth anything as short as possible.
func RedeemToken(token string) (*Session, uint64, bool) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	e, ok := tokens[token]
	if !ok {
		return nil, 0, false
	}
	delete(tokens, token)
	if time.Now().After(e.expiresAt) || e.gen != passwordGen {
		return nil, 0, false
	}
	return e.session, e.gen, true
}

// RevokeAllTokensFor removes every outstanding token bound to ses (matched
// by pointer identity, like every rotation descending from the same
// original password-based login) - used when the owner explicitly signs
// out, so a token that was already sitting in some other tab's storage
// can't silently keep working afterwards.
func RevokeAllTokensFor(ses *Session) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	for _, k := range bySession[ses] {
		delete(tokens, k)
	}
	delete(bySession, ses)
}

// liveLocked is list without the tokens no longer held.
func liveLocked(list []string) []string {
	out := list[:0]
	for _, k := range list {
		if _, ok := tokens[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// dropOldestLocked drops the oldest token held; false when there is none.
func dropOldestLocked() bool {
	for len(issued) > 0 {
		k := issued[0]
		issued = issued[1:]
		if _, ok := tokens[k]; ok {
			delete(tokens, k)
			return true
		}
	}
	return false
}

// dropExpiredLocked drops, oldest first, the tokens past their TTL.
func dropExpiredLocked(now time.Time) {
	for len(issued) > 0 {
		e, ok := tokens[issued[0]]
		if ok && !now.After(e.expiresAt) {
			return
		}
		if ok {
			delete(tokens, issued[0])
		}
		issued = issued[1:]
	}
}

// compactIssuedLocked keeps issued from growing with tokens redeemed or
// revoked long before they would expire: once most of it is leftovers it
// is rebuilt from what is held.
func compactIssuedLocked() {
	if len(issued) <= 2*len(tokens)+64 {
		return
	}
	issued = liveLocked(issued)
}

// purgeEvery sweeps expired tokens every d, and the sessions left with
// none (a session is never kept alive by this store).
func purgeEvery(d time.Duration) {
	for range time.Tick(d) {
		purgeExpired(time.Now())
	}
}

// purgeExpired drops what has expired by now.
func purgeExpired(now time.Time) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	dropExpiredLocked(now)
	for ses, list := range bySession {
		if live := liveLocked(list); len(live) > 0 {
			bySession[ses] = live
		} else {
			delete(bySession, ses)
		}
	}
	compactIssuedLocked()
}
