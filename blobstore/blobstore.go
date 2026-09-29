// SPDX-License-Identifier: AGPL-3.0-or-later

// Package blobstore reads and writes the device's encrypted files (blobs
// and their thumbnails) in the segmented format of package segcrypt, so a
// read decrypts only the part it needs and a write never holds more than
// one segment. Files written before the format existed (one AES-GCM seal
// over the whole content) are still read, whole, until they're converted
// (see Convert).
package blobstore

import (
	"bytes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alonsovidales/otc/segcrypt"
)

// Keys is what opening and sealing need from a signed-in session.
type Keys interface {
	AEAD() cipher.AEAD
	Decrypt([]byte) ([]byte, error)
}

// Blob is an open encrypted file: its content, readable at any offset.
type Blob interface {
	io.ReaderAt
	Size() int64
	Close() error
}

type segBlob struct {
	*segcrypt.Reader
	f *os.File
}

func (b *segBlob) Close() error { return b.f.Close() }

type memBlob struct{ *bytes.Reader }

func (memBlob) Close() error { return nil }

// Open opens the encrypted file at path.
func Open(path string, k Keys) (Blob, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	head := make([]byte, 4)
	if _, err := f.ReadAt(head, 0); err == nil && segcrypt.IsSegmented(head) {
		r, err := segcrypt.NewReader(f, info.Size(), k.AEAD())
		if err != nil {
			f.Close()
			return nil, err
		}
		return &segBlob{Reader: r, f: f}, nil
	}
	// The old format: one seal over everything, so it can only be opened
	// whole. Converted to segments by Convert.
	enc, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	plain, err := k.Decrypt(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypting %s: %w", filepath.Base(path), err)
	}
	return memBlob{bytes.NewReader(plain)}, nil
}

// ReadAll is the whole content of the encrypted file at path - for what
// needs all of it at once (decoding a photo, a thumbnail).
func ReadAll(path string, k Keys) ([]byte, error) {
	b, err := Open(path, k)
	if err != nil {
		return nil, err
	}
	defer b.Close()
	out := make([]byte, b.Size())
	if _, err := b.ReadAt(out, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return out, nil
}

// Writer seals content into a temp file next to target; Commit moves it
// into place (a reader never sees a half-written file), Abort discards it.
type Writer struct {
	target string
	tmp    *os.File
	seg    *segcrypt.Writer
	done   bool
}

// Create starts sealing a new file for target.
func Create(target string, k Keys) (*Writer, error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".blob-*")
	if err != nil {
		return nil, err
	}
	seg, err := segcrypt.NewWriter(tmp, k.AEAD())
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	return &Writer{target: target, tmp: tmp, seg: seg}, nil
}

func (w *Writer) Write(p []byte) (int, error) { return w.seg.Write(p) }

// CommitAs is Commit under another name in the same directory - for an
// upload, whose name (its content hash) is known only once it's complete.
func (w *Writer) CommitAs(target string) error {
	w.target = target
	return w.Commit()
}

// Seal seals the last segment without moving the file into place yet
// (then CommitAs or Abort).
func (w *Writer) Seal() error { return w.seg.Close() }

// Commit seals the last segment, syncs, and renames the file into place.
func (w *Writer) Commit() error {
	if w.done {
		return errors.New("blobstore: already finished")
	}
	w.done = true
	err := w.seg.Close() // a no-op if Seal already ran
	if err == nil {
		err = w.tmp.Sync()
	}
	if cerr := w.tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(w.tmp.Name(), 0o644) // perms: rw-r--r-- (ciphertext)
	}
	if err == nil {
		err = os.Rename(w.tmp.Name(), w.target)
	}
	if err != nil {
		os.Remove(w.tmp.Name())
	}
	return err
}

// Abort discards the file.
func (w *Writer) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.tmp.Close()
	os.Remove(w.tmp.Name())
}

// WriteBytes seals content into target in one go.
func WriteBytes(target string, k Keys, content []byte) error {
	w, err := Create(target, k)
	if err != nil {
		return err
	}
	if _, err := w.Write(content); err != nil {
		w.Abort()
		return err
	}
	return w.Commit()
}

// IsSegmented reports whether the file at path is already in the
// segmented format.
func IsSegmented(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return false, nil
	}
	return segcrypt.IsSegmented(head), nil
}

// Convert rewrites a file in the old whole-seal format as segments (in
// place, atomically); a no-op for one already converted.
func Convert(path string, k Keys) (converted bool, err error) {
	seg, err := IsSegmented(path)
	if err != nil || seg {
		return false, err
	}
	enc, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	plain, err := k.Decrypt(enc)
	if err != nil {
		return false, fmt.Errorf("decrypting %s: %w", filepath.Base(path), err)
	}
	return true, WriteBytes(path, k, plain)
}
