// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"sync"
	"testing"
	"time"
)

// newTestSession builds a Session with a deterministic cipher, bypassing
// New() (which requires a live *dao.Dao) since Session's fields are
// accessible from within this package.
func newTestSession(t *testing.T, key string) *Session {
	t.Helper()

	keyHash := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}

	return &Session{Uuid: "test-uuid", cipher: gcm}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")
	plain := []byte("some secret content to protect")

	enc := ses.Encrypt(plain)
	if bytes.Equal(enc, plain) {
		t.Fatal("Encrypt returned the plaintext unchanged")
	}

	dec, err := ses.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt returned an error: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Errorf("round trip mismatch: got %q, want %q", dec, plain)
	}
}

func TestEncryptProducesUniqueNonces(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")
	plain := []byte("same content, twice")

	first := ses.Encrypt(plain)
	second := ses.Encrypt(plain)

	if bytes.Equal(first, second) {
		t.Error("expected two encryptions of the same content to differ (nonce should be unique per call)")
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	encrypter := newTestSession(t, "key-one")
	decrypter := newTestSession(t, "key-two")

	enc := encrypter.Encrypt([]byte("top secret"))

	if _, err := decrypter.Decrypt(enc); err == nil {
		t.Error("expected Decrypt with the wrong key to fail, it succeeded")
	}
}

func TestDecryptTamperedContentFails(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")
	enc := ses.Encrypt([]byte("authenticate me"))

	tampered := append([]byte{}, enc...)
	tampered[len(tampered)-1] ^= 0xFF // flip the last byte of the auth tag

	if _, err := ses.Decrypt(tampered); err == nil {
		t.Error("expected Decrypt to reject tampered ciphertext, it succeeded")
	}
}

func TestDecryptShortContentFails(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")

	if _, err := ses.Decrypt([]byte("short")); err == nil {
		t.Error("expected Decrypt to reject content shorter than the nonce size")
	}
}

func TestDecryptEmptyContentFails(t *testing.T) {
	ses := newTestSession(t, "some-secret-key")

	if _, err := ses.Decrypt(nil); err == nil {
		t.Error("expected Decrypt to reject empty content")
	}
}

// A burst of sign-ins after a restart must not run more than two Argon2
// derivations (64 MiB each) at once.
func TestDeriveWrappingKeyIsBounded(t *testing.T) {
	var running, peak int32
	var mu sync.Mutex
	var wg sync.WaitGroup
	orig := deriveSlots
	defer func() { deriveSlots = orig }()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deriveSlots <- struct{}{}
			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			<-deriveSlots
		}()
	}
	wg.Wait()
	if peak > int32(cap(deriveSlots)) || cap(deriveSlots) > 2 {
		t.Errorf("up to %d derivations ran at once (limit %d)", peak, cap(deriveSlots))
	}
	a := deriveWrappingKey("pw", []byte("salt-salt-salt-1"))
	if a != deriveWrappingKey("pw", []byte("salt-salt-salt-1")) {
		t.Error("the derivation must stay deterministic")
	}
}
