// SPDX-License-Identifier: AGPL-3.0-or-later

// Package segcrypt is how file content is encrypted on the device: in
// fixed-size segments, each sealed on its own, so any range of a file can
// be read by decrypting only the segments it covers - a video streamed a
// few MB at a time, a file downloaded in chunks, an upload encrypted as it
// arrives - instead of holding the whole file in memory to seal or open it
// in one piece (as the device used to, and ran out of memory doing).
//
// Layout of an encrypted file (OTS2, what every file is written in since
// release 164):
//
//	header   "OTS2" | salt (32 random bytes)
//	segment  AES-GCM(SegmentSize bytes of content) + 16-byte tag, repeated
//	last     AES-GCM(0..SegmentSize bytes) + tag - always present, even
//	         for empty content
//
// Each file is sealed under a key of its own: HKDF-SHA256 of the key it
// belongs to (the data key, or a share link's) with the file's salt and
// fileKeyInfo, 32 bytes, AES-256-GCM. Segment i is sealed with the nonce
// 7 zero bytes | i (4 bytes, big endian) | last-flag (1 byte) - a key
// seals one file, so the nonces need nothing else - and the header as
// additional data (the STREAM construction): segments can't be reordered,
// dropped, moved between files, and the file can't be cut short - a
// truncated file's new last segment was sealed as "not last" and fails to
// open. One file on disk per blob; segment i starts at the header's size
// + i*(SegmentSize+TagSize).
//
// OTS1, what releases 40 to 163 wrote, is still read and never written:
//
//	header   "OTS1" | nonce prefix (7 random bytes)
//
// with the same segments, sealed under the key itself and with the nonce
// prefix in place of the zeros. Two OTS1 files that drew the same prefix
// share every nonce under one key, which gives away the XOR of their
// contents and the key's GCM authentication key (issue #203); a key of
// their own per file is what OTS2 adds.
package segcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	// MagicSize is how much of a file's start names its format (FormatOf).
	MagicSize = 4
	// SaltSize is the length of an OTS2 file's salt.
	SaltSize = 32
	// MaxHeaderSize is the longest header any format has.
	MaxHeaderSize = MagicSize + SaltSize
	SegmentSize   = 1 << 20
	TagSize       = 16
	sealedSize    = SegmentSize + TagSize
	// prefixSize is OTS1's nonce prefix; an OTS2 nonce starts with as many
	// zeros.
	prefixSize = 7
	// fileKeyInfo is HKDF's info for an OTS2 file's key.
	fileKeyInfo = "otc segcrypt OTS2 file key"
)

// ErrFormat: not a segmented file (or a damaged one).
var ErrFormat = errors.New("segcrypt: not a segmented encrypted file")

// Format is a file's layout, named by its first MagicSize bytes.
type Format uint8

const (
	// OTS1: every file sealed under the key itself (releases 40 to 163).
	OTS1 Format = 1
	// OTS2: every file sealed under a key of its own (release 164 on).
	OTS2 Format = 2
	// Current is the format new files are written in.
	Current = OTS2
)

func (f Format) magic() string {
	switch f {
	case OTS1:
		return "OTS1"
	case OTS2:
		return "OTS2"
	}
	return ""
}

func (f Format) String() string {
	if m := f.magic(); m != "" {
		return m
	}
	return fmt.Sprintf("Format(%d)", uint8(f))
}

// HeaderSize is the size of a file's header in format f (0 for no format).
func (f Format) HeaderSize() int {
	switch f {
	case OTS1:
		return MagicSize + prefixSize
	case OTS2:
		return MagicSize + SaltSize
	}
	return 0
}

// FormatOf is the format of a file whose first bytes are head (at least
// MagicSize of them); ErrFormat when it isn't one of this package's.
func FormatOf(head []byte) (Format, error) {
	if len(head) >= MagicSize {
		for _, f := range []Format{OTS1, OTS2} {
			if string(head[:MagicSize]) == f.magic() {
				return f, nil
			}
		}
	}
	return 0, ErrFormat
}

// IsSegmented reports whether head (the first bytes of a file) starts like
// one written by this package, in either format.
func IsSegmented(head []byte) bool {
	_, err := FormatOf(head)
	return err == nil
}

// Keys is the key a file is sealed under - the data key, or a share
// link's.
type Keys interface {
	// AEAD is the key's own AES-GCM cipher, which sealed OTS1 files.
	AEAD() cipher.AEAD
	// FileAEAD is the cipher of the key of their own that OTS2 files with
	// this salt are sealed under (Key.FileAEAD).
	FileAEAD(salt []byte) (cipher.AEAD, error)
}

// Key is the Keys of a 32-byte AES-256 key held as its bytes.
type Key struct {
	raw  []byte
	aead cipher.AEAD
}

// NewKey is the Keys of key, 32 bytes.
func NewKey(key []byte) (*Key, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("segcrypt: a key of %d bytes, want 32", len(key))
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return &Key{raw: bytes.Clone(key), aead: aead}, nil
}

func (k *Key) AEAD() cipher.AEAD { return k.aead }

// FileAEAD derives an OTS2 file's own key from k and the file's salt
// (HKDF-SHA256, fileKeyInfo) and gives its AES-256-GCM cipher.
func (k *Key) FileAEAD(salt []byte) (cipher.AEAD, error) {
	if len(salt) != SaltSize {
		return nil, fmt.Errorf("segcrypt: a salt of %d bytes, want %d", len(salt), SaltSize)
	}
	fileKey, err := hkdf.Key(sha256.New, k.raw, salt, fileKeyInfo, 32)
	if err != nil {
		return nil, err
	}
	return newGCM(fileKey)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// cipherFor is what a file in format f with this header is sealed with:
// the AES-GCM cipher and the first 7 bytes of every nonce (nil: zeros).
func (f Format) cipherFor(k Keys, header []byte) (cipher.AEAD, []byte, error) {
	var aead cipher.AEAD
	var prefix []byte
	switch f {
	case OTS1:
		aead, prefix = k.AEAD(), header[MagicSize:]
	case OTS2:
		var err error
		if aead, err = k.FileAEAD(header[MagicSize:]); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, ErrFormat
	}
	if aead == nil || aead.NonceSize() != 12 {
		return nil, nil, errors.New("segcrypt: the cipher must be AES-GCM with 12-byte nonces")
	}
	return aead, prefix, nil
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
	prefix []byte
	buf    []byte
	next   uint32
	closed bool
}

// NewWriter writes the header of a new file (Current's format, under a
// salt of its own) to w and returns the Writer.
func NewWriter(w io.Writer, k Keys) (*Writer, error) {
	return newWriter(w, k, Current)
}

// newWriter writes a file in format f: Current, or OTS1 for the tests of
// files written before it.
func newWriter(w io.Writer, k Keys, f Format) (*Writer, error) {
	header := make([]byte, f.HeaderSize())
	if len(header) == 0 {
		return nil, ErrFormat
	}
	copy(header, f.magic())
	if _, err := io.ReadFull(rand.Reader, header[MagicSize:]); err != nil {
		return nil, err
	}
	aead, prefix, err := f.cipherFor(k, header)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	// buf is allocated at the first Write: an upload begun and never sent
	// to held a whole segment for nothing.
	return &Writer{w: w, aead: aead, header: header, prefix: prefix}, nil
}

func (s *Writer) seal(data []byte, last bool) error {
	out := s.aead.Seal(nil, nonce(s.prefix, s.next, last), data, s.header)
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

// EncryptedSize is the size on disk of plain bytes of content sealed in
// format f: Current's for a file about to be written, the file's own
// (FormatOf) for one on disk - the two headers differ in size.
func (f Format) EncryptedSize(plain int64) int64 {
	segments := plain / SegmentSize
	if plain%SegmentSize != 0 || plain == 0 {
		segments++
	}
	return int64(f.HeaderSize()) + plain + segments*TagSize
}

// layout is how many segments an encrypted file of format f and encSize
// bytes holds, and how much content. Only the sizes EncryptedSize gives
// are a file's: a last segment after full ones holds at least a byte, as
// the Writer seals a full segment only once more content follows.
func (f Format) layout(encSize int64) (segments, plain int64, err error) {
	header := int64(f.HeaderSize())
	if header == 0 || encSize < header+TagSize {
		return 0, 0, ErrFormat
	}
	body := encSize - header
	segments = body / sealedSize
	if body%sealedSize != 0 {
		segments++
	}
	plain = body - segments*TagSize
	if plain < 0 || (segments > 1 && body-(segments-1)*sealedSize <= TagSize) {
		return 0, 0, ErrFormat
	}
	return segments, plain, nil
}

// PlainSize is the size of the content of an encrypted file of format f
// (FormatOf its first bytes) and encSize bytes on disk - EncryptedSize's
// inverse, what NewReader's Size reports - without a key or reading
// further.
func (f Format) PlainSize(encSize int64) (int64, error) {
	_, plain, err := f.layout(encSize)
	return plain, err
}

// Reader decrypts a segmented file on demand: ReadAt opens only the
// segments a read covers. Safe for concurrent use.
type Reader struct {
	r        io.ReaderAt
	aead     cipher.AEAD
	header   []byte
	prefix   []byte
	segments int64
	size     int64

	mu       sync.Mutex
	cachedI  int64
	cached   []byte
	cacheSet bool
}

// NewReader reads the header of the encrypted file r (of encSize bytes),
// in either format.
func NewReader(r io.ReaderAt, encSize int64, k Keys) (*Reader, error) {
	if encSize < MagicSize {
		return nil, ErrFormat
	}
	// The longest header there is, or the whole file when it is shorter
	// (an empty OTS1 file is 27 bytes): its magic says how much of it is
	// the header.
	head := make([]byte, min(encSize, MaxHeaderSize))
	if n, err := r.ReadAt(head, 0); n < len(head) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	f, err := FormatOf(head)
	if err != nil {
		return nil, err
	}
	segments, plain, err := f.layout(encSize)
	if err != nil {
		return nil, err
	}
	header := head[:f.HeaderSize()] // layout: encSize holds a header and a tag
	aead, prefix, err := f.cipherFor(k, header)
	if err != nil {
		return nil, err
	}
	d := &Reader{r: r, aead: aead, header: header, prefix: prefix, segments: segments, size: plain}
	// No read of empty content opens a segment, so its one segment is
	// opened now: a file cut down to a header and 16 bytes isn't taken for
	// an empty one.
	if plain == 0 {
		if _, err := d.segment(0); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Size is the content's size.
func (d *Reader) Size() int64 { return d.size }

func (d *Reader) segment(i int64) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cacheSet && d.cachedI == i {
		return d.cached, nil
	}
	header := int64(len(d.header))
	start := header + i*sealedSize
	n := int64(sealedSize)
	if i == d.segments-1 {
		n = header + d.size + d.segments*TagSize - start
	}
	enc := make([]byte, n)
	if _, err := d.r.ReadAt(enc, start); err != nil && !(errors.Is(err, io.EOF) && int64(len(enc)) == n) {
		return nil, err
	}
	plain, err := d.aead.Open(enc[:0], nonce(d.prefix, uint32(i), i == d.segments-1), enc, d.header)
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
