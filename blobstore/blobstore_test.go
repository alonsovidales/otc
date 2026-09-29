// SPDX-License-Identifier: AGPL-3.0-or-later

package blobstore

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// keys mimics a session: the segmented format uses the AEAD directly, the
// old one was nonce | seal(content).
type keys struct{ aead cipher.AEAD }

func (k keys) AEAD() cipher.AEAD { return k.aead }
func (k keys) Decrypt(b []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(b) < n {
		return nil, errors.New("short")
	}
	return k.aead.Open(nil, b[:n], b[n:], nil)
}
func (k keys) legacy(content []byte) []byte {
	nonce := make([]byte, k.aead.NonceSize())
	rand.Read(nonce)
	return k.aead.Seal(nonce, nonce, content, nil)
}

func newKeys() keys {
	key := make([]byte, 32)
	rand.Read(key)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	return keys{aead}
}

func TestOldFilesReadAndConvert(t *testing.T) {
	k := newKeys()
	dir := t.TempDir()
	path := filepath.Join(dir, "abc")
	content := bytes.Repeat([]byte("photo "), 400000) // > 2 segments
	if err := os.WriteFile(path, k.legacy(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Read in the old format...
	if got, err := ReadAll(path, k); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("old format read: %v", err)
	}
	// ...converted in place...
	if ok, err := Convert(path, k); err != nil || !ok {
		t.Fatalf("Convert: %v %v", ok, err)
	}
	if seg, _ := IsSegmented(path); !seg {
		t.Fatal("not segmented after Convert")
	}
	if got, err := ReadAll(path, k); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("read after conversion: %v", err)
	}
	// ...and a second conversion is a no-op.
	if ok, err := Convert(path, k); err != nil || ok {
		t.Fatalf("second Convert: %v %v", ok, err)
	}
	// A file under another key is left alone.
	other := filepath.Join(dir, "zip")
	os.WriteFile(other, newKeys().legacy([]byte("x")), 0o644)
	if _, err := Convert(other, k); err == nil {
		t.Error("a file under another key was converted")
	}
}
