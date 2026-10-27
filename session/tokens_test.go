// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"testing"
	"time"
)

// Issue #101: these pin down the token store's actual security properties
// - not just "it returns a token" - since it's standing in for what used
// to be the real password in browser storage.

// freshTokens empties the store for one test and again after it.
func freshTokens(t *testing.T) {
	t.Helper()
	reset := func() {
		tokenMu.Lock()
		tokens, issued, bySession = map[string]*tokenEntry{}, nil, map[*Session][]string{}
		tokenMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func issue(t *testing.T, ses *Session) string {
	t.Helper()
	token, _, err := IssueToken(ses, PasswordGeneration())
	if err != nil {
		t.Fatalf("IssueToken returned an error: %v", err)
	}
	return token
}

func held(token string) bool {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	_, ok := tokens[token]
	return ok
}

func TestIssueThenRedeemTokenReturnsTheSameSession(t *testing.T) {
	freshTokens(t)
	ses := newTestSession(t, "some-secret-key")

	token, expiresAt, err := IssueToken(ses, PasswordGeneration())
	if err != nil {
		t.Fatalf("IssueToken returned an error: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Error("expected expiresAt to be in the future")
	}

	got, gen, ok := RedeemToken(token)
	if !ok {
		t.Fatal("expected RedeemToken to succeed for a freshly issued token")
	}
	if got != ses {
		t.Error("expected RedeemToken to return the exact Session it was issued for")
	}
	if gen != PasswordGeneration() {
		t.Errorf("redeemed under generation %d, want the current %d", gen, PasswordGeneration())
	}
}

// A leaked token is only as dangerous as the single redemption it's worth
// - the whole point of not just replaying the same token forever.
func TestRedeemTokenIsSingleUse(t *testing.T) {
	freshTokens(t)
	token := issue(t, newTestSession(t, "some-secret-key"))

	if _, _, ok := RedeemToken(token); !ok {
		t.Fatal("expected the first redemption to succeed")
	}
	if _, _, ok := RedeemToken(token); ok {
		t.Error("expected a second redemption of the same token to fail")
	}
}

func TestRedeemTokenFailsForUnknownToken(t *testing.T) {
	if _, _, ok := RedeemToken("not-a-real-token"); ok {
		t.Error("expected RedeemToken to fail for a token that was never issued")
	}
}

// The TTL is what makes this strictly safer than the old "password with a
// client-side timestamp check" scheme - here expiry is enforced by the
// server holding the token, not by whether the browser felt like checking.
func TestRedeemTokenFailsOncePastItsTTL(t *testing.T) {
	freshTokens(t)
	token := issue(t, newTestSession(t, "some-secret-key"))
	tokenMu.Lock()
	tokens[token].expiresAt = time.Now().Add(-time.Second)
	tokenMu.Unlock()

	if _, _, ok := RedeemToken(token); ok {
		t.Error("expected RedeemToken to reject an already-expired token")
	}
}

func TestRevokeAllTokensForOnlyRemovesThatSessionsTokens(t *testing.T) {
	freshTokens(t)
	sesA := newTestSession(t, "key-a")
	sesB := newTestSession(t, "key-b")

	tokenA1, tokenA2, tokenB := issue(t, sesA), issue(t, sesA), issue(t, sesB)

	RevokeAllTokensFor(sesA)

	if _, _, ok := RedeemToken(tokenA1); ok {
		t.Error("expected sesA's first token to be revoked")
	}
	if _, _, ok := RedeemToken(tokenA2); ok {
		t.Error("expected sesA's second token to be revoked")
	}
	if _, _, ok := RedeemToken(tokenB); !ok {
		t.Error("expected sesB's own token to be unaffected by revoking sesA's tokens")
	}
}

// Expired tokens go without a request asking for one - the sweep a ticker
// runs - and a session left with none isn't kept by the store.
func TestPurgeDropsExpiredTokensAndTheirSessions(t *testing.T) {
	freshTokens(t)
	old, kept := newTestSession(t, "old"), newTestSession(t, "kept")
	stale := issue(t, old)
	fresh := issue(t, kept)
	tokenMu.Lock()
	tokens[stale].expiresAt = time.Now().Add(-time.Minute)
	tokenMu.Unlock()

	purgeExpired(time.Now())

	if held(stale) {
		t.Error("expected the expired token to have been purged")
	}
	if !held(fresh) {
		t.Error("a token still valid was purged")
	}
	tokenMu.Lock()
	_, oldKept := bySession[old]
	_, keptKept := bySession[kept]
	tokenMu.Unlock()
	if oldKept || !keptKept {
		t.Errorf("sessions kept: old %v (want false), kept %v (want true)", oldKept, keptKept)
	}
}

// The demo device's password is public: anyone signed in can ask for tokens
// without end. One sign-in holds cMaxTokensPerSession at most, its oldest
// going first, and another's are untouched.
func TestTokensPerSessionAreCapped(t *testing.T) {
	freshTokens(t)
	ses, other := newTestSession(t, "busy"), newTestSession(t, "other")
	theirs := issue(t, other)
	var mine []string
	for i := 0; i < cMaxTokensPerSession+5; i++ {
		mine = append(mine, issue(t, ses))
	}
	for i, tok := range mine {
		if want := i >= 5; held(tok) != want {
			t.Errorf("token %d held %v, want %v", i, held(tok), want)
		}
	}
	if !held(theirs) {
		t.Error("another session's token went")
	}
	tokenMu.Lock()
	n := len(bySession[ses])
	tokenMu.Unlock()
	if n != cMaxTokensPerSession {
		t.Errorf("%d tokens listed for the session, want %d", n, cMaxTokensPerSession)
	}
}

// And all of them together hold cMaxTokens at most, the oldest going first;
// the order kept for that doesn't grow with tokens long redeemed.
func TestTokensInAllAreCapped(t *testing.T) {
	freshTokens(t)
	first := issue(t, newTestSession(t, "first"))
	for i := 0; i < cMaxTokens; i++ {
		issue(t, newTestSession(t, "many"))
	}
	tokenMu.Lock()
	n := len(tokens)
	tokenMu.Unlock()
	if n != cMaxTokens {
		t.Errorf("%d tokens held, want %d", n, cMaxTokens)
	}
	if held(first) {
		t.Error("the oldest token was kept past the cap")
	}

	freshTokens(t)
	ses := newTestSession(t, "rotating")
	for i := 0; i < 1000; i++ {
		if _, _, ok := RedeemToken(issue(t, ses)); !ok {
			t.Fatal("redeem failed")
		}
	}
	tokenMu.Lock()
	order := len(issued)
	tokenMu.Unlock()
	if order > 100 {
		t.Errorf("the issue order holds %d tokens for none held", order)
	}
}

// A password change ends every token, and nothing signed in before it gets
// or redeems one any more.
func TestPasswordChangeEndsEveryToken(t *testing.T) {
	freshTokens(t)
	ses, other := newTestSession(t, "changer"), newTestSession(t, "elsewhere")
	before := PasswordGeneration()
	mine, theirs := issue(t, ses), issue(t, other)

	gen := PasswordChanged()
	if gen == before {
		t.Fatal("the password generation didn't move")
	}
	for _, tok := range []string{mine, theirs} {
		if _, _, ok := RedeemToken(tok); ok {
			t.Error("a token from before the change still redeems")
		}
	}
	if _, _, err := IssueToken(other, before); err != ErrStaleSession {
		t.Errorf("a session signed in before the change got a token: %v", err)
	}
	tok, _, err := IssueToken(ses, gen)
	if err != nil {
		t.Fatalf("the session that changed it, moved to the new generation: %v", err)
	}
	if _, g, ok := RedeemToken(tok); !ok || g != gen {
		t.Errorf("a token of the new generation: ok %v, generation %d (want %d)", ok, g, gen)
	}
}

// A token issued just before the change but redeemed after it is refused
// too, even if it was never dropped (one issued in the same instant).
func TestRedeemRefusesATokenOfAnOlderGeneration(t *testing.T) {
	freshTokens(t)
	ses := newTestSession(t, "k")
	tok := issue(t, ses)
	tokenMu.Lock()
	e := tokens[tok]
	passwordGen++
	tokens[tok] = e // kept, as a change racing this token's issue would
	tokenMu.Unlock()
	if _, _, ok := RedeemToken(tok); ok {
		t.Error("a token of an older generation redeemed")
	}
}
