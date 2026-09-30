// SPDX-License-Identifier: AGPL-3.0-or-later

// Package blobstore reads and writes the device's encrypted files (blobs
// and their thumbnails) in the segmented format of package segcrypt, so a
// read decrypts only the part it needs and a write never holds more than
// one segment. Files written before the format existed (one AES-GCM seal
// over the whole content) are still read, whole, until they're converted
// (see Convert).
package blobstore

import (
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alonsovidales/otc/segcrypt"
)

// Keys is what opening and sealing need from a signed-in session: its
// data-key cipher.
type Keys interface {
	AEAD() cipher.AEAD
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
	r, err := segcrypt.NewReader(f, info.Size(), k.AEAD())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	return &segBlob{Reader: r, f: f}, nil
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
		err = os.Chmod(w.tmp.Name(), 0o600) // perms: rw------- (issue #157: only the service reads it)
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
