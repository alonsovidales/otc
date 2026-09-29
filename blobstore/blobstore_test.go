// SPDX-License-Identifier: AGPL-3.0-or-later

package blobstore

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

type keys struct{ aead cipher.AEAD }

func (k keys) AEAD() cipher.AEAD { return k.aead }

func newKeys() keys {
	key := make([]byte, 32)
	rand.Read(key)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	return keys{aead}
}

func TestWriteOpenAndCommitAs(t *testing.T) {
	k := newKeys()
	dir := t.TempDir()
	content := bytes.Repeat([]byte("photo "), 400000) // > 2 segments
	path := filepath.Join(dir, "abc")
	if err := WriteBytes(path, k, content); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadAll(path, k); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("read back: %v", err)
	}

	// An upload: sealed as it arrives, named only at the end.
	w, err := Create(filepath.Join(dir, ".pending"), k)
	if err != nil {
		t.Fatal(err)
	}
	for p := content; len(p) > 0; {
		n := min(len(p), 100000)
		w.Write(p[:n])
		p = p[n:]
	}
	if err := w.Seal(); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, "byhash")
	if err := w.CommitAs(final); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadAll(final, k); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("committed upload: %v", err)
	}
	// Nothing left behind but the two files.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("%d files in the directory, want 2", len(entries))
	}

	// Another key can't open it.
	if _, err := ReadAll(path, newKeys()); err == nil {
		t.Error("opened with the wrong key")
	}
}
