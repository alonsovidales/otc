package filesmanager

import (
	"bytes"
	"math"
	"testing"
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
