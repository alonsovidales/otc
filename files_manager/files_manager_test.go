package filesmanager

import (
	"bytes"
	"math"
	"sync"
	"testing"
	"time"
)

func TestCosineSimilarityIdenticalVectors(t *testing.T) {
	mg := &Manager{}
	a := []float32{1, 2, 3}

	got := mg.cosineSimilarity(a, a)
	if math.Abs(float64(got-1)) > 1e-6 {
		t.Errorf("expected cosine similarity of a vector with itself to be ~1, got %v", got)
	}
}

func TestCosineSimilarityOrthogonalVectors(t *testing.T) {
	mg := &Manager{}
	a := []float32{1, 0}
	b := []float32{0, 1}

	got := mg.cosineSimilarity(a, b)
	if math.Abs(float64(got)) > 1e-6 {
		t.Errorf("expected cosine similarity of orthogonal vectors to be ~0, got %v", got)
	}
}

func TestCosineSimilarityOppositeVectors(t *testing.T) {
	mg := &Manager{}
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}

	got := mg.cosineSimilarity(a, b)
	if math.Abs(float64(got+1)) > 1e-6 {
		t.Errorf("expected cosine similarity of opposite vectors to be ~-1, got %v", got)
	}
}

func TestGetCipherRoundTrip(t *testing.T) {
	cp := getCipher("some-secret")

	nonce := make([]byte, cp.NonceSize())
	plain := []byte("round trip me")
	enc := cp.Seal(nonce, nonce, plain, nil)

	dec, err := cp.Open(nil, enc[:cp.NonceSize()], enc[cp.NonceSize():], nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Errorf("round trip mismatch: got %q, want %q", dec, plain)
	}
}

func TestGetCipherIsDeterministicPerSecret(t *testing.T) {
	a := getCipher("same-secret")
	b := getCipher("same-secret")

	nonce := make([]byte, a.NonceSize())
	plain := []byte("data")
	encA := a.Seal(nonce, nonce, plain, nil)

	// A cipher derived from the same secret must be able to decrypt data
	// sealed by another cipher derived from that same secret.
	dec, err := b.Open(nil, encA[:b.NonceSize()], encA[b.NonceSize():], nil)
	if err != nil {
		t.Fatalf("expected ciphers derived from the same secret to interoperate: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Errorf("round trip mismatch: got %q, want %q", dec, plain)
	}
}

func TestGetCipherDifferentSecretsDoNotInteroperate(t *testing.T) {
	a := getCipher("secret-one")
	b := getCipher("secret-two")

	nonce := make([]byte, a.NonceSize())
	enc := a.Seal(nonce, nonce, []byte("data"), nil)

	if _, err := b.Open(nil, enc[:b.NonceSize()], enc[b.NonceSize():], nil); err == nil {
		t.Error("expected a cipher derived from a different secret to fail to decrypt")
	}
}

func TestIsSharedLinkExpired(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour

	cases := []struct {
		name    string
		created time.Time
		want    bool
	}{
		{"well within ttl", now.Add(-time.Hour), false},
		{"just under ttl", now.Add(-ttl + time.Minute), false},
		{"exactly at ttl boundary", now.Add(-ttl), false},
		{"just past ttl", now.Add(-ttl - time.Minute), true},
		{"long expired", now.Add(-30 * 24 * time.Hour), true},
		{"created in the future", now.Add(time.Hour), false},
	}

	for _, c := range cases {
		if got := isSharedLinkExpired(c.created, now, ttl); got != c.want {
			t.Errorf("%s: isSharedLinkExpired() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCollectExpiredTokensRemovesOnlyExpiredEntries(t *testing.T) {
	mg := &Manager{
		searchTokens:   new(sync.Map),
		tokensToExpire: new(sync.Map),
	}

	now := time.Now()
	mg.searchTokens.Store("fresh", "fresh-value")
	mg.tokensToExpire.Store("fresh", now)

	mg.searchTokens.Store("stale", "stale-value")
	mg.tokensToExpire.Store("stale", now.Add(-cToeknsTTL-time.Minute))

	mg.collectExpiredTokens()

	if _, ok := mg.searchTokens.Load("fresh"); !ok {
		t.Error("expected the fresh token to survive collection")
	}
	if _, ok := mg.tokensToExpire.Load("fresh"); !ok {
		t.Error("expected the fresh token's expiry entry to survive collection")
	}
	if _, ok := mg.searchTokens.Load("stale"); ok {
		t.Error("expected the stale token to be removed")
	}
	if _, ok := mg.tokensToExpire.Load("stale"); ok {
		t.Error("expected the stale token's expiry entry to be removed")
	}
}

func TestSharedLinkTTLFromHours(t *testing.T) {
	cases := []struct {
		hours int64
		want  time.Duration
	}{
		{0, cDefaultSharedLinkTTL},  // unset key parses to 0 -> default
		{-5, cDefaultSharedLinkTTL}, // defensive: never a negative TTL
		{1, time.Hour},
		{168, 168 * time.Hour},
	}

	for _, c := range cases {
		if got := sharedLinkTTLFromHours(c.hours); got != c.want {
			t.Errorf("sharedLinkTTLFromHours(%d) = %v, want %v", c.hours, got, c.want)
		}
	}
}
