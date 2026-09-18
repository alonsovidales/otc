// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"crypto/rand"
	"encoding/base64"
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
const (
	// TokenTTL matches the web client's previous cPersistedKeyTTLMs, so
	// this change doesn't shorten how long a browser tab stays signed in.
	TokenTTL = time.Hour

	cTokenBytes = 32
)

type tokenEntry struct {
	session   *Session
	expiresAt time.Time
}

var (
	tokenMu sync.Mutex
	tokens  = map[string]*tokenEntry{}
)

func randomToken() (string, error) {
	b := make([]byte, cTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// IssueToken mints a fresh opaque token bound to ses, valid for TokenTTL.
// Called both right after a password-based Auth and every time a client
// redeems a still-valid token (RedeemToken below) - each redemption gets a
// brand new token with a fresh TTL rather than counting down from the
// original login, so a browser tab that's reloaded regularly never has to
// fall back to the password prompt, matching what the old
// savePersistedKey(key)-on-every-successful-auth behavior already did.
func IssueToken(ses *Session) (token string, expiresAt time.Time, err error) {
	token, err = randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(TokenTTL)

	tokenMu.Lock()
	defer tokenMu.Unlock()
	purgeExpiredLocked()
	tokens[token] = &tokenEntry{session: ses, expiresAt: expiresAt}
	return token, expiresAt, nil
}

// RedeemToken looks up token and, if it's still valid, returns the Session
// it was issued for. Single-use: a successful redemption removes it
// immediately, so a client must call IssueToken again for whatever it
// persists next (see ReqAuthWithToken's handler) - this keeps the window
// in which a leaked token is still worth anything as short as possible.
func RedeemToken(token string) (*Session, bool) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	purgeExpiredLocked()
	e, ok := tokens[token]
	if !ok {
		return nil, false
	}
	delete(tokens, token)
	return e.session, true
}

// RevokeAllTokensFor removes every outstanding token bound to ses (matched
// by pointer identity, like every rotation descending from the same
// original password-based login) - used when the owner explicitly signs
// out, so a token that was already sitting in some other tab's storage
// can't silently keep working afterwards.
func RevokeAllTokensFor(ses *Session) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	for k, e := range tokens {
		if e.session == ses {
			delete(tokens, k)
		}
	}
}

// purgeExpiredLocked drops stale entries so a long-running device doesn't
// accumulate an unbounded map of dead tokens from tabs that were simply
// never reloaded again. Callers must already hold tokenMu.
func purgeExpiredLocked() {
	now := time.Now()
	for k, e := range tokens {
		if now.After(e.expiresAt) {
			delete(tokens, k)
		}
	}
}
