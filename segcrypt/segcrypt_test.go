// SPDX-License-Identifier: AGPL-3.0-or-later

package segcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	mrand "math/rand"
	"testing"
)

var formats = []Format{OTS1, OTS2}

func testKey(t testing.TB) *Key {
	t.Helper()
	raw := make([]byte, 32)
	rand.Read(raw)
	k, err := NewKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// sealIn seals plain in format f: OTS2 through NewWriter, as the device
// writes, OTS1 as releases before 164 did.
func sealIn(t testing.TB, k Keys, f Format, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	var w *Writer
	var err error
	if f == Current {
		w, err = NewWriter(&out, k)
	} else {
		w, err = newWriter(&out, k, f)
	}
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

func readAll(k Keys, enc []byte) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(enc), int64(len(enc)), k)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r.Stream())
}

// Both formats: what is written reads back whole and by range, at exactly
// the size EncryptedSize predicts for that format.
func TestRoundTripAndRanges(t *testing.T) {
	k := testKey(t)
	for _, f := range formats {
		for _, size := range []int{0, 1, SegmentSize - 1, SegmentSize, SegmentSize + 1, 3*SegmentSize + 12345} {
			plain := make([]byte, size)
			rand.Read(plain)
			enc := sealIn(t, k, f, plain)
			if got, err := FormatOf(enc); err != nil || got != f {
				t.Fatalf("%v size %d: written as %v, %v", f, size, got, err)
			}
			if int64(len(enc)) != f.EncryptedSize(int64(size)) {
				t.Errorf("%v size %d: encrypted %d bytes, EncryptedSize says %d", f, size, len(enc), f.EncryptedSize(int64(size)))
			}
			r, err := NewReader(bytes.NewReader(enc), int64(len(enc)), k)
			if err != nil {
				t.Fatalf("%v size %d: %v", f, size, err)
			}
			if r.Size() != int64(size) {
				t.Fatalf("%v size %d: Reader.Size %d", f, size, r.Size())
			}
			all, err := io.ReadAll(r.Stream())
			if err != nil || !bytes.Equal(all, plain) {
				t.Fatalf("%v size %d: full read differs (%v)", f, size, err)
			}
			for i := 0; i < 20 && size > 0; i++ {
				off := mrand.Intn(size)
				n := mrand.Intn(size-off) + 1
				buf := make([]byte, n)
				if _, err := r.ReadAt(buf, int64(off)); err != nil && err != io.EOF {
					t.Fatalf("%v size %d: ReadAt(%d,%d): %v", f, size, off, n, err)
				}
				if !bytes.Equal(buf, plain[off:off+n]) {
					t.Fatalf("%v size %d: ReadAt(%d,%d) differs", f, size, off, n)
				}
			}
		}
	}
}

// New files are OTS2, and NewWriter's prediction is Current's.
func TestNewFilesAreCurrent(t *testing.T) {
	if Current != OTS2 {
		t.Fatalf("Current is %v", Current)
	}
	enc := sealIn(t, testKey(t), Current, []byte("photo"))
	if string(enc[:MagicSize]) != "OTS2" || len(enc) != OTS2.HeaderSize()+5+TagSize {
		t.Errorf("a new file starts %q and has %d bytes", enc[:MagicSize], len(enc))
	}
}

func TestTamperingIsDetected(t *testing.T) {
	k := testKey(t)
	plain := make([]byte, 2*SegmentSize+100)
	rand.Read(plain)
	for _, f := range formats {
		enc := sealIn(t, k, f, plain)
		h := f.HeaderSize()
		// Cut at a segment boundary: the new last segment was sealed as "not last".
		if _, err := readAll(k, enc[:h+2*sealedSize]); err == nil {
			t.Errorf("%v: a file cut at a segment boundary read fine", f)
		}
		// Cut down to its header and 16 bytes: not an empty file.
		if _, err := NewReader(bytes.NewReader(enc[:h+TagSize]), int64(h+TagSize), k); err == nil {
			t.Errorf("%v: a file cut to its header and a tag opened as empty", f)
		}
		// A flipped byte, in a segment and in the header (the salt or the
		// nonce prefix).
		for _, at := range []int{h + 10, MagicSize + 3} {
			flipped := bytes.Clone(enc)
			flipped[at] ^= 1
			if _, err := readAll(k, flipped); err == nil {
				t.Errorf("%v: a byte flipped at %d read fine", f, at)
			}
		}
		// A segment from another file (same key) put in its place.
		other := sealIn(t, k, f, plain)
		swapped := bytes.Clone(enc)
		copy(swapped[h:h+sealedSize], other[h:h+sealedSize])
		if _, err := readAll(k, swapped); err == nil {
			t.Errorf("%v: a segment from another file read fine", f)
		}
		// Two segments of the same file swapped.
		reordered := bytes.Clone(enc)
		copy(reordered[h:h+sealedSize], enc[h+sealedSize:h+2*sealedSize])
		copy(reordered[h+sealedSize:h+2*sealedSize], enc[h:h+sealedSize])
		if _, err := readAll(k, reordered); err == nil {
			t.Errorf("%v: reordered segments read fine", f)
		}
		// Another key.
		if _, err := readAll(testKey(t), enc); err == nil {
			t.Errorf("%v: read with another key", f)
		}
	}
	// One format's file taken for the other's.
	for _, f := range formats {
		enc := sealIn(t, k, f, []byte("content"))
		other := OTS1
		if f == OTS1 {
			other = OTS2
		}
		copy(enc, other.magic())
		if _, err := readAll(k, enc); err == nil {
			t.Errorf("an %v file read as %v", f, other)
		}
	}
	// An OTS2 file whose salt is another file's: another key.
	a, b := sealIn(t, k, OTS2, plain[:100]), sealIn(t, k, OTS2, plain[:100])
	copy(a[MagicSize:OTS2.HeaderSize()], b[MagicSize:OTS2.HeaderSize()])
	if _, err := readAll(k, a); err == nil {
		t.Error("a file read under another file's salt")
	}
	// Not our format at all.
	if _, err := NewReader(bytes.NewReader(make([]byte, 100)), 100, k); err == nil {
		t.Error("random bytes were taken for a segmented file")
	}
}

// Issue #203: one library holds files of both formats under the same key,
// and each is read by its own header.
func TestMixedLibrary(t *testing.T) {
	k := testKey(t)
	type file struct {
		f          Format
		plain, enc []byte
	}
	var library []file
	for i, size := range []int{0, 7, 4096, SegmentSize, SegmentSize + 3, 2*SegmentSize - 1} {
		f := formats[i%2]
		plain := make([]byte, size)
		rand.Read(plain)
		library = append(library, file{f, plain, sealIn(t, k, f, plain)})
	}
	for _, x := range library {
		got, err := readAll(k, x.enc)
		if err != nil || !bytes.Equal(got, x.plain) {
			t.Errorf("%v file of %d bytes: %v", x.f, len(x.plain), err)
		}
		f, err := FormatOf(x.enc)
		if err != nil || f != x.f {
			t.Errorf("%v file of %d bytes taken for %v (%v)", x.f, len(x.plain), f, err)
		}
		if n, err := f.PlainSize(int64(len(x.enc))); err != nil || n != int64(len(x.plain)) {
			t.Errorf("%v file of %d bytes: PlainSize %d, %v", x.f, len(x.plain), n, err)
		}
	}
}

// A file release 163 wrote (key 00 01 .. 1f), byte for byte: still read.
func TestReadsWhatRelease163Wrote(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	k, err := NewKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	for plain, file := range map[string]string{
		"":                           "4f545331fdacbaa517535797925ada198e87eeeaf93a809e86e292",
		"Off The Cloud, release 163": "4f545331593d6eb3b943164cdb329828cc0b6859c48b7a49be45a8a8813911ae5d5175cc133c24c11b31c4f4534993a62821797bc4",
	} {
		enc, _ := hex.DecodeString(file)
		got, err := readAll(k, enc)
		if err != nil || string(got) != plain {
			t.Errorf("%q: read %q, %v", plain, got, err)
		}
	}
}

// OTS2 as the package comment describes it, opened by hand: the salt,
// HKDF-SHA256 with fileKeyInfo, nonces 7 zeros | index | last, the header
// as additional data.
func TestOTS2IsWhatItSays(t *testing.T) {
	raw := make([]byte, 32)
	rand.Read(raw)
	k, _ := NewKey(raw)
	plain := make([]byte, SegmentSize+10)
	rand.Read(plain)
	enc := sealIn(t, k, OTS2, plain)
	header, salt := enc[:MagicSize+SaltSize], enc[MagicSize:MagicSize+SaltSize]
	fileKey, err := hkdf.Key(sha256.New, raw, salt, "otc segcrypt OTS2 file key", 32)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(fileKey)
	aead, _ := cipher.NewGCM(block)
	var got []byte
	rest := enc[len(header):]
	for i := 0; len(rest) > 0; i++ {
		seg := rest[:min(len(rest), sealedSize)]
		rest = rest[len(seg):]
		n := make([]byte, 12)
		binary.BigEndian.PutUint32(n[7:], uint32(i))
		if len(rest) == 0 {
			n[11] = 1
		}
		p, err := aead.Open(nil, n, seg, header)
		if err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
		got = append(got, p...)
	}
	if !bytes.Equal(got, plain) {
		t.Error("the content opened by hand differs")
	}
	// Not the key itself, and another salt for every file.
	if _, err := k.AEAD().Open(nil, make([]byte, 12), enc[len(header):], header); err == nil {
		t.Error("an OTS2 file opened under the key itself")
	}
	if again := sealIn(t, k, OTS2, plain); bytes.Equal(again[MagicSize:MagicSize+SaltSize], salt) {
		t.Error("two files drew the same salt")
	}
}

func TestKeys(t *testing.T) {
	if _, err := NewKey(make([]byte, 16)); err == nil {
		t.Error("a 16-byte key was taken")
	}
	k := testKey(t)
	salt := make([]byte, SaltSize)
	rand.Read(salt)
	for _, bad := range [][]byte{nil, salt[:31], append(bytes.Clone(salt), 0)} {
		if _, err := k.FileAEAD(bad); err == nil {
			t.Errorf("a salt of %d bytes was taken", len(bad))
		}
	}
	// The same key and salt make the same file key; another salt another.
	a, _ := k.FileAEAD(salt)
	b, _ := k.FileAEAD(bytes.Clone(salt))
	salt[0] ^= 1
	c, _ := k.FileAEAD(salt)
	n := make([]byte, 12)
	sealed := a.Seal(nil, n, []byte("x"), nil)
	if _, err := b.Open(nil, n, sealed, nil); err != nil {
		t.Error("the same salt gave another key")
	}
	if _, err := c.Open(nil, n, sealed, nil); err == nil {
		t.Error("another salt gave the same key")
	}
}

func TestFormatOf(t *testing.T) {
	for head, want := range map[string]Format{"OTS1": OTS1, "OTS2xyz": OTS2, "OTS1\x00\x00": OTS1} {
		if f, err := FormatOf([]byte(head)); err != nil || f != want {
			t.Errorf("FormatOf(%q) = %v, %v", head, f, err)
		}
	}
	for _, head := range []string{"", "OTS", "OTS3", "ots1", "\x00\x00\x00\x00"} {
		if f, err := FormatOf([]byte(head)); err == nil || IsSegmented([]byte(head)) {
			t.Errorf("FormatOf(%q) = %v", head, f)
		}
	}
	if Format(0).HeaderSize() != 0 || Format(3).HeaderSize() != 0 {
		t.Error("an unknown format has a header")
	}
	if _, err := Format(0).PlainSize(1000); err == nil {
		t.Error("an unknown format has a plain size")
	}
	if OTS1.HeaderSize() != 11 || OTS2.HeaderSize() != 36 || MaxHeaderSize != 36 {
		t.Errorf("headers %d and %d bytes, at most %d", OTS1.HeaderSize(), OTS2.HeaderSize(), MaxHeaderSize)
	}
}

// Issue #187: the size backfill reads a blob's content size from its size
// on disk, so PlainSize must invert EncryptedSize, 2 GiB and up included,
// and agree with what NewReader reports - each format by its own header
// (issue #203: the same size on disk is different content in the two).
func TestPlainSize(t *testing.T) {
	for _, f := range formats {
		for _, plain := range []int64{0, 1, SegmentSize - 1, SegmentSize, SegmentSize + 1, 1<<31 - 1, 1 << 31, 3 << 30, 5<<30 + 12345, 40 * SegmentSize} {
			got, err := f.PlainSize(f.EncryptedSize(plain))
			if err != nil || got != plain {
				t.Errorf("%v: PlainSize(EncryptedSize(%d)) = %d, %v", f, plain, got, err)
			}
		}
		// Sizes no segmented file can have: shorter than a header and a
		// tag, a last segment too short for its tag, or an empty one after
		// full ones.
		h := int64(f.HeaderSize())
		for _, enc := range []int64{-1, 0, h, h + TagSize - 1, h + sealedSize + TagSize - 1, h + sealedSize + TagSize, h + 3*sealedSize + TagSize} {
			if n, err := f.PlainSize(enc); err == nil {
				t.Errorf("%v: PlainSize(%d) = %d, want an error", f, enc, n)
			}
		}
		k := testKey(t)
		for _, size := range []int{0, 5, SegmentSize, 2*SegmentSize + 7} {
			enc := sealIn(t, k, f, make([]byte, size))
			r, err := NewReader(bytes.NewReader(enc), int64(len(enc)), k)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := f.PlainSize(int64(len(enc))); err != nil || n != r.Size() {
				t.Errorf("%v size %d: PlainSize %d, %v; Reader.Size %d", f, size, n, err, r.Size())
			}
		}
	}
	if a, b := OTS1.EncryptedSize(1000), OTS2.EncryptedSize(1000); b-a != int64(OTS2.HeaderSize()-OTS1.HeaderSize()) {
		t.Errorf("1000 bytes take %d as OTS1 and %d as OTS2", a, b)
	}
	n1, _ := OTS1.PlainSize(5000)
	n2, _ := OTS2.PlainSize(5000)
	if n1 == n2 {
		t.Error("5000 bytes on disk hold the same content in both formats")
	}
}

// FuzzHeader: whatever a file's first bytes and its size on disk (a stat
// taken before the file changed may disagree with them) - a header cut
// short, a magic and nothing more, a size no file has - its sizes are
// sound or an error and opening it is an error, never a panic, unless its
// first size bytes are a whole file sealed under the key (what follows
// them is never read).
func FuzzHeader(f *testing.F) {
	raw := bytes.Repeat([]byte{7}, 32)
	k, _ := NewKey(raw)
	for _, format := range formats {
		for _, size := range []int{0, 1, 100} {
			enc := sealIn(f, k, format, bytes.Repeat([]byte{'a'}, size))
			for cut := 0; cut <= len(enc); cut++ {
				f.Add(enc[:cut], int64(0))
			}
			f.Add(enc, int64(sealedSize))
			f.Add(enc, int64(-1))
			f.Add(append(bytes.Clone(enc), "junk"...), int64(-4))
		}
	}
	f.Add([]byte("OTS2"), int64(1<<40))
	f.Add([]byte("OTS1"), int64(1<<62))
	f.Fuzz(func(t *testing.T, data []byte, extra int64) {
		encSize := int64(len(data)) + extra
		if format, err := FormatOf(data); err == nil {
			if n, err := format.PlainSize(encSize); err == nil {
				if n < 0 || n > encSize || format.EncryptedSize(n) != encSize {
					t.Fatalf("%v: PlainSize(%d) = %d, which seals to %d", format, encSize, n, format.EncryptedSize(n))
				}
			}
		} else if IsSegmented(data) {
			t.Fatal("IsSegmented disagrees with FormatOf")
		}
		r, err := NewReader(bytes.NewReader(data), encSize, k)
		if err != nil {
			return
		}
		got, err := io.ReadAll(io.LimitReader(r.Stream(), 4*SegmentSize))
		if err != nil {
			return
		}
		format, _ := FormatOf(data)
		if encSize > int64(len(data)) || format.EncryptedSize(int64(len(got))) != encSize || !bytes.Equal(got, bytes.Repeat([]byte{'a'}, len(got))) {
			t.Fatalf("%d bytes (size %d) opened to %d bytes", len(data), encSize, len(got))
		}
	})
}
