// SPDX-License-Identifier: AGPL-3.0-or-later

package segcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
	mrand "math/rand"
	"testing"
)

func testAEAD(t *testing.T) cipher.AEAD {
	t.Helper()
	key := make([]byte, 32)
	rand.Read(key)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	return aead
}

func seal(t *testing.T, aead cipher.AEAD, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewWriter(&out, aead)
	if err != nil {
		t.Fatal(err)
	}
	// Written in odd-sized pieces, as uploads arrive.
	for p := plain; len(p) > 0; {
		n := min(len(p), 333333)
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestRoundTripAndRanges(t *testing.T) {
	aead := testAEAD(t)
	for _, size := range []int{0, 1, SegmentSize - 1, SegmentSize, SegmentSize + 1, 3*SegmentSize + 12345} {
		plain := make([]byte, size)
		rand.Read(plain)
		enc := seal(t, aead, plain)
		if int64(len(enc)) != EncryptedSize(int64(size)) {
			t.Errorf("size %d: encrypted %d bytes, EncryptedSize says %d", size, len(enc), EncryptedSize(int64(size)))
		}
		r, err := NewReader(bytes.NewReader(enc), int64(len(enc)), aead)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if r.Size() != int64(size) {
			t.Fatalf("size %d: Reader.Size %d", size, r.Size())
		}
		all, err := io.ReadAll(r.Stream())
		if err != nil || !bytes.Equal(all, plain) {
			t.Fatalf("size %d: full read differs (%v)", size, err)
		}
		for k := 0; k < 20 && size > 0; k++ {
			off := mrand.Intn(size)
			n := mrand.Intn(size-off) + 1
			buf := make([]byte, n)
			if _, err := r.ReadAt(buf, int64(off)); err != nil && err != io.EOF {
				t.Fatalf("size %d: ReadAt(%d,%d): %v", size, off, n, err)
			}
			if !bytes.Equal(buf, plain[off:off+n]) {
				t.Fatalf("size %d: ReadAt(%d,%d) differs", size, off, n)
			}
		}
	}
}

func TestTamperingIsDetected(t *testing.T) {
	aead := testAEAD(t)
	plain := make([]byte, 2*SegmentSize+100)
	rand.Read(plain)
	enc := seal(t, aead, plain)

	readAll := func(b []byte) error {
		r, err := NewReader(bytes.NewReader(b), int64(len(b)), aead)
		if err != nil {
			return err
		}
		_, err = io.ReadAll(r.Stream())
		return err
	}

	// Cut at a segment boundary: the new last segment was sealed as "not last".
	cut := enc[:HeaderSize+2*sealedSize]
	if readAll(cut) == nil {
		t.Error("a file cut at a segment boundary read fine")
	}
	// A flipped byte.
	flipped := append([]byte(nil), enc...)
	flipped[HeaderSize+10] ^= 1
	if readAll(flipped) == nil {
		t.Error("a tampered segment read fine")
	}
	// A segment from another file (same key) put in its place.
	other := seal(t, aead, plain)
	swapped := append([]byte(nil), enc...)
	copy(swapped[HeaderSize:HeaderSize+sealedSize], other[HeaderSize:HeaderSize+sealedSize])
	if readAll(swapped) == nil {
		t.Error("a segment from another file read fine")
	}
	// Two segments of the same file swapped.
	reordered := append([]byte(nil), enc...)
	copy(reordered[HeaderSize:HeaderSize+sealedSize], enc[HeaderSize+sealedSize:HeaderSize+2*sealedSize])
	copy(reordered[HeaderSize+sealedSize:HeaderSize+2*sealedSize], enc[HeaderSize:HeaderSize+sealedSize])
	if readAll(reordered) == nil {
		t.Error("reordered segments read fine")
	}
	// Not our format at all.
	if _, err := NewReader(bytes.NewReader(make([]byte, 100)), 100, aead); err == nil {
		t.Error("random bytes were taken for a segmented file")
	}
}
