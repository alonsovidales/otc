// SPDX-License-Identifier: AGPL-3.0-or-later

package blobstore

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/alonsovidales/otc/segcrypt"
)

func newKeys() *segcrypt.Key {
	key := make([]byte, 32)
	rand.Read(key)
	k, _ := segcrypt.NewKey(key)
	return k
}

// writeOTS1 writes content at path as releases 40 to 163 did, by hand:
// "OTS1" and a random nonce prefix, then 1 MiB segments sealed under the
// key itself with nonce prefix | index | last-flag and the header as
// additional data.
func writeOTS1(t *testing.T, path string, aead cipher.AEAD, content []byte) {
	t.Helper()
	header := append([]byte("OTS1"), make([]byte, 7)...)
	rand.Read(header[4:])
	out := bytes.Clone(header)
	for i := 0; ; i++ {
		n := min(len(content), segcrypt.SegmentSize)
		last := n == len(content)
		nonce := make([]byte, 12)
		copy(nonce, header[4:])
		binary.BigEndian.PutUint32(nonce[7:], uint32(i))
		if last {
			nonce[11] = 1
		}
		out = aead.Seal(out, nonce, content[:n], header)
		content = content[n:]
		if last {
			break
		}
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
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

// Issue #203: what is written now is OTS2, at exactly the size predicted
// for a new file; what releases before 164 wrote (OTS1) is read next to it
// with the same key, each file by its own header.
func TestMixedLibrary(t *testing.T) {
	k := newKeys()
	dir := t.TempDir()
	type file struct {
		name    string
		format  segcrypt.Format
		content []byte
	}
	var library []file
	for i, size := range []int{0, 5, segcrypt.SegmentSize, 2*segcrypt.SegmentSize + 9} {
		content := make([]byte, size)
		rand.Read(content)
		old, cur := filepath.Join(dir, "old", string(rune('a'+i))), filepath.Join(dir, "new", string(rune('a'+i)))
		os.MkdirAll(filepath.Dir(old), 0o700)
		os.MkdirAll(filepath.Dir(cur), 0o700)
		writeOTS1(t, old, k.AEAD(), content)
		if err := WriteBytes(cur, k, content); err != nil {
			t.Fatal(err)
		}
		library = append(library, file{old, segcrypt.OTS1, content}, file{cur, segcrypt.OTS2, content})
	}
	for _, f := range library {
		got, err := ReadAll(f.name, k)
		if err != nil || !bytes.Equal(got, f.content) {
			t.Errorf("%v file of %d bytes: %v", f.format, len(f.content), err)
		}
		format, err := FormatOf(f.name)
		if err != nil || format != f.format {
			t.Errorf("%v file of %d bytes is %v (%v)", f.format, len(f.content), format, err)
		}
		fi, _ := os.Stat(f.name)
		if want := f.format.EncryptedSize(int64(len(f.content))); fi.Size() != want {
			t.Errorf("%v file of %d bytes takes %d on the disk, its format says %d", f.format, len(f.content), fi.Size(), want)
		}
		if f.format == segcrypt.OTS2 && fi.Size() != segcrypt.Current.EncryptedSize(int64(len(f.content))) {
			t.Errorf("a new file of %d bytes takes %d, not the %d predicted", len(f.content), fi.Size(), segcrypt.Current.EncryptedSize(int64(len(f.content))))
		}
	}
}

func TestFormatOfAFile(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"short": "OT", "empty": "", "other": "PK\x03\x04 a zip"} {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
		if f, err := FormatOf(filepath.Join(dir, name)); !errors.Is(err, segcrypt.ErrFormat) {
			t.Errorf("%s: %v, %v", name, f, err)
		}
	}
	if _, err := FormatOf(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
}
