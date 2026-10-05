// SPDX-License-Identifier: AGPL-3.0-or-later

// Package segcrypt is how file content is encrypted on the device: in
// fixed-size segments, each sealed on its own, so any range of a file can
// be read by decrypting only the segments it covers - a video streamed a
// few MB at a time, a file downloaded in chunks, an upload encrypted as it
// arrives - instead of holding the whole file in memory to seal or open it
// in one piece (as the device used to, and ran out of memory doing).
//
// Layout of an encrypted file:
//
//	header   "OTS1" | nonce prefix (7 random bytes)
//	segment  AES-GCM(SegmentSize bytes of content) + 16-byte tag, repeated
//	last     AES-GCM(0..SegmentSize bytes) + tag - always present, even
//	         for empty content
//
// Segment i is sealed with the nonce prefix | i (4 bytes, big endian) |
// last-flag (1 byte), and the header as additional data (the STREAM
// construction): segments can't be reordered, dropped, moved between
// files, and the file can't be cut short - a truncated file's new last
// segment was sealed as "not last" and fails to open. One file on disk per
// blob; segment i starts at HeaderSize + i*(SegmentSize+TagSize).
package segcrypt

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	magic       = "OTS1"
	prefixSize  = 7
	HeaderSize  = len(magic) + prefixSize
	SegmentSize = 1 << 20
	TagSize     = 16
	sealedSize  = SegmentSize + TagSize
)

// ErrFormat: not a segmented file (or a damaged one).
var ErrFormat = errors.New("segcrypt: not a segmented encrypted file")

// IsSegmented reports whether head (the first bytes of a file) starts like
// one written by this package.
func IsSegmented(head []byte) bool {
	return len(head) >= len(magic) && string(head[:len(magic)]) == magic
}

func nonce(prefix []byte, i uint32, last bool) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[prefixSize:], i)
	if last {
		n[11] = 1
	}
	return n
}

// Writer encrypts what is written to it, segment by segment, into w. Close
// seals the last segment; nothing is complete without it.
type Writer struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	buf    []byte
	next   uint32
	closed bool
}

// NewWriter writes the header to w and returns the Writer. aead must be
// AES-GCM (12-byte nonces).
func NewWriter(w io.Writer, aead cipher.AEAD) (*Writer, error) {
	if aead.NonceSize() != 12 {
		return nil, fmt.Errorf("segcrypt: nonce size %d, want 12", aead.NonceSize())
	}
	header := make([]byte, HeaderSize)
	copy(header, magic)
	if _, err := io.ReadFull(rand.Reader, header[len(magic):]); err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	// buf is allocated at the first Write: an upload begun and never sent
	// to held a whole segment for nothing.
	return &Writer{w: w, aead: aead, header: header}, nil
}

func (s *Writer) seal(data []byte, last bool) error {
	out := s.aead.Seal(nil, nonce(s.header[len(magic):], s.next, last), data, s.header)
	if _, err := s.w.Write(out); err != nil {
		return err
	}
	s.next++
	return nil
}

func (s *Writer) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("segcrypt: write after close")
	}
	n := len(p)
	if s.buf == nil && n > 0 {
		s.buf = make([]byte, 0, SegmentSize)
	}
	for len(p) > 0 {
		room := SegmentSize - len(s.buf)
		take := min(room, len(p))
		s.buf = append(s.buf, p[:take]...)
		p = p[take:]
		// A full segment is sealed only once more content follows: the
		// last one must be sealed as last, at Close.
		if len(s.buf) == SegmentSize && len(p) > 0 {
			if err := s.seal(s.buf, false); err != nil {
				return n - len(p), err
			}
			s.buf = s.buf[:0]
		}
	}
	return n, nil
}

// Close seals the last segment. It doesn't close the underlying writer.
func (s *Writer) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.seal(s.buf, true)
}

// EncryptedSize is the size on disk of plain bytes of content.
func EncryptedSize(plain int64) int64 {
	segments := plain / SegmentSize
	if plain%SegmentSize != 0 || plain == 0 {
		segments++
	}
	return int64(HeaderSize) + plain + segments*TagSize
}

// Reader decrypts a segmented file on demand: ReadAt opens only the
// segments a read covers. Safe for concurrent use.
type Reader struct {
	r        io.ReaderAt
	aead     cipher.AEAD
	header   []byte
	segments int64
	size     int64

	mu       sync.Mutex
	cachedI  int64
	cached   []byte
	cacheSet bool
}

// NewReader reads the header of the encrypted file r (of encSize bytes).
func NewReader(r io.ReaderAt, encSize int64, aead cipher.AEAD) (*Reader, error) {
	if encSize < int64(HeaderSize+TagSize) {
		return nil, ErrFormat
	}
	header := make([]byte, HeaderSize)
	if _, err := r.ReadAt(header, 0); err != nil {
		return nil, err
	}
	if !IsSegmented(header) {
		return nil, ErrFormat
	}
	body := encSize - int64(HeaderSize)
	segments := (body + sealedSize - 1) / sealedSize
	plain := body - segments*TagSize
	if plain < 0 || (segments > 1 && body-(segments-1)*sealedSize < TagSize) {
		return nil, ErrFormat
	}
	return &Reader{r: r, aead: aead, header: header, segments: segments, size: plain}, nil
}

// Size is the content's size.
func (d *Reader) Size() int64 { return d.size }

func (d *Reader) segment(i int64) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cacheSet && d.cachedI == i {
		return d.cached, nil
	}
	start := int64(HeaderSize) + i*sealedSize
	n := int64(sealedSize)
	if i == d.segments-1 {
		n = int64(HeaderSize) + d.size + d.segments*TagSize - start
	}
	enc := make([]byte, n)
	if _, err := d.r.ReadAt(enc, start); err != nil && !(errors.Is(err, io.EOF) && int64(len(enc)) == n) {
		return nil, err
	}
	plain, err := d.aead.Open(enc[:0], nonce(d.header[len(magic):], uint32(i), i == d.segments-1), enc, d.header)
	if err != nil {
		return nil, fmt.Errorf("segcrypt: segment %d: %w", i, err)
	}
	d.cachedI, d.cached, d.cacheSet = i, plain, true
	return plain, nil
}

// ReadAt reads len(p) bytes of content from off (io.ReaderAt).
func (d *Reader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("segcrypt: negative offset")
	}
	if off >= d.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && off < d.size {
		i := off / SegmentSize
		seg, err := d.segment(i)
		if err != nil {
			return n, err
		}
		c := copy(p[n:], seg[off-i*SegmentSize:])
		n += c
		off += int64(c)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Stream is the content as an io.Reader from the start.
func (d *Reader) Stream() io.Reader { return io.NewSectionReader(d, 0, d.size) }
