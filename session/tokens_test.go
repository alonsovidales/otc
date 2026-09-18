// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"testing"
	"time"
)

// Issue #101: these pin down the token store's actual security properties
// - not just "it returns a token" - since it's standing in for what used
// to be the real password in browser storage.

func TestIssueThenRedeemTokenReturnsTheSameSession(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")

	token, expiresAt, err := IssueToken(ses)
	if err != nil {
		t.Fatalf("IssueToken returned an error: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Error("expected expiresAt to be in the future")
	}

	got, ok := RedeemToken(token)
	if !ok {
		t.Fatal("expected RedeemToken to succeed for a freshly issued token")
	}
	if got != ses {
		t.Error("expected RedeemToken to return the exact Session it was issued for")
	}
}

// A leaked token is only as dangerous as the single redemption it's worth
// - the whole point of not just replaying the same token forever.
func TestRedeemTokenIsSingleUse(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")
	token, _, err := IssueToken(ses)
	if err != nil {
		t.Fatalf("IssueToken returned an error: %v", err)
	}

	if _, ok := RedeemToken(token); !ok {
		t.Fatal("expected the first redemption to succeed")
	}
	if _, ok := RedeemToken(token); ok {
		t.Error("expected a second redemption of the same token to fail")
	}
}

func TestRedeemTokenFailsForUnknownToken(t *testing.T) {
	if _, ok := RedeemToken("not-a-real-token"); ok {
		t.Error("expected RedeemToken to fail for a token that was never issued")
	}
}

// The TTL is what makes this strictly safer than the old "password with a
// client-side timestamp check" scheme - here expiry is enforced by the
// server holding the token, not by whether the browser felt like checking.
func TestRedeemTokenFailsOncePastItsTTL(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")
	token, err := randomToken()
	if err != nil {
		t.Fatalf("randomToken: %v", err)
	}

	tokenMu.Lock()
	tokens[token] = &tokenEntry{session: ses, expiresAt: time.Now().Add(-time.Second)}
	tokenMu.Unlock()

	if _, ok := RedeemToken(token); ok {
		t.Error("expected RedeemToken to reject an already-expired token")
	}
}

func TestRevokeAllTokensForOnlyRemovesThatSessionsTokens(t *testing.T) {
	sesA := newTestSession(t, "key-a")
	sesB := newTestSession(t, "key-b")

	tokenA1, _, err := IssueToken(sesA)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	tokenA2, _, err := IssueToken(sesA)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	tokenB, _, err := IssueToken(sesB)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	RevokeAllTokensFor(sesA)

	if _, ok := RedeemToken(tokenA1); ok {
		t.Error("expected sesA's first token to be revoked")
	}
	if _, ok := RedeemToken(tokenA2); ok {
		t.Error("expected sesA's second token to be revoked")
	}
	if _, ok := RedeemToken(tokenB); !ok {
		t.Error("expected sesB's own token to be unaffected by revoking sesA's tokens")
	}
}

func TestIssueTokenPurgesExpiredEntries(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")
	staleToken, err := randomToken()
	if err != nil {
		t.Fatalf("randomToken: %v", err)
	}

	tokenMu.Lock()
	tokens[staleToken] = &tokenEntry{session: ses, expiresAt: time.Now().Add(-time.Minute)}
	tokenMu.Unlock()

	// Issuing a new token piggybacks a purge of anything already expired.
	if _, _, err := IssueToken(ses); err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	tokenMu.Lock()
	_, stillThere := tokens[staleToken]
	tokenMu.Unlock()
	if stillThere {
		t.Error("expected the expired token to have been purged")
	}
}
