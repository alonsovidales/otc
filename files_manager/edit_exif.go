// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/jdeng/goheif"
)

// A photo edit (edit_image.go) is written by Go's JPEG encoder, which
// writes the pixels and nothing else: the camera's details - the date it
// was taken, where, with what - and the colour profile would be lost, and
// with them the photo's place in Images and Places, and its colours on a
// wide-gamut screen. They are carried over from the original here: its
// EXIF (a JPEG's APP1, a HEIC's Exif item) with the orientation set to the
// one the edit leaves and the dimensions to the edit's, and its ICC
// profile (a JPEG's APP2 segments, a HEIC's colr box).

const (
	cExifHeader = "Exif\x00\x00"
	cICCHeader  = "ICC_PROFILE\x00"
	// cMaxSegment is the most a JPEG marker segment carries after its
	// length.
	cMaxSegment = 65533
	// cICCChunk is the most of a profile one APP2 segment carries: the
	// segment less its header, sequence number and count.
	cICCChunk = cMaxSegment - len(cICCHeader) - 2
)

// jpegSegment is one marker segment before a JPEG's scan: marker (0xE1 for
// APP1) and its payload, without the length, which starts at at.
type jpegSegment struct {
	marker  byte
	payload []byte
	at      int
}

// jpegHeaderSegments is the marker segments of b before its first scan,
// and the offset where the rest (from that scan's SOS, or the first
// segment without a length) starts. An error for what isn't a JPEG.
func jpegHeaderSegments(b []byte) (segs []jpegSegment, rest int, err error) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, 0, errors.New("not a JPEG") // i18n-ignore: an edit's internal error, logged
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return nil, 0, errors.New("a JPEG marker is missing") // i18n-ignore: an edit's internal error, logged
		}
		marker := b[i+1]
		if marker == 0xFF { // fill bytes before a marker
			i++
			continue
		}
		if marker == 0xDA || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			return segs, i, nil
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return nil, 0, errors.New("a JPEG segment runs past the end") // i18n-ignore: an edit's internal error, logged
		}
		segs = append(segs, jpegSegment{marker: marker, payload: b[i+4 : i+2+n], at: i + 4})
		i += 2 + n
	}
	return segs, len(b), nil
}

// jpegMetadata is a JPEG's EXIF, as the TIFF structure after "Exif\0\0", or
// nil; and its ICC profile's APP2 segments, as they are.
func jpegMetadata(b []byte) (exif []byte, icc [][]byte) {
	segs, _, err := jpegHeaderSegments(b)
	if err != nil {
		return nil, nil
	}
	for _, s := range segs {
		switch {
		case s.marker == 0xE1 && exif == nil && bytes.HasPrefix(s.payload, []byte(cExifHeader)):
			exif = s.payload[len(cExifHeader):]
		case s.marker == 0xE2 && bytes.HasPrefix(s.payload, []byte(cICCHeader)):
			icc = append(icc, s.payload)
		}
	}
	return exif, icc
}

// heicMetadata is a HEIC's EXIF (the TIFF structure) and its ICC profile
// (whole, from its colr box), each nil when it has none.
func heicMetadata(b []byte) (exif []byte, icc []byte) {
	if raw, err := goheif.ExtractExif(bytes.NewReader(b)); err == nil {
		// The item is "Exif\0\0" and the TIFF structure, or the TIFF
		// structure alone (goheif already dropped the item's offset).
		if i := bytes.Index(raw, []byte(cExifHeader)); i >= 0 && i < 16 {
			raw = raw[i+len(cExifHeader):]
		}
		if len(raw) >= 8 && (bytes.HasPrefix(raw, []byte("II*\x00")) || bytes.HasPrefix(raw, []byte("MM\x00*"))) {
			exif = raw
		}
	}
	return exif, heifICC(b)
}

// heifICC is the ICC profile of a HEIF's colr box ('prof' or 'rICC'), nil
// when it has none (an nclx colr, or none): looked for in the boxes before
// the picture data, and believed only with an ICC profile's signature.
func heifICC(b []byte) []byte {
	end := min(len(b), 1<<20)
	for i := 4; i+8 <= end; {
		j := bytes.Index(b[i:end], []byte("colr"))
		if j < 0 {
			return nil
		}
		at := i + j
		size := int(binary.BigEndian.Uint32(b[at-4:]))
		kind := string(b[at+4 : min(at+8, len(b))])
		if (kind == "prof" || kind == "rICC") && size > 12+128 && at-4+size <= len(b) {
			icc := b[at+8 : at-4+size]
			if len(icc) >= 40 && string(icc[36:40]) == "acsp" {
				return icc
			}
		}
		i = at + 4
	}
	return nil
}

// iccSegments is an ICC profile as the APP2 segments a JPEG carries it in.
func iccSegments(icc []byte) [][]byte {
	if len(icc) == 0 {
		return nil
	}
	count := (len(icc) + cICCChunk - 1) / cICCChunk
	if count > 255 {
		return nil
	}
	segs := make([][]byte, 0, count)
	for n := 0; n < count; n++ {
		chunk := icc[n*cICCChunk : min((n+1)*cICCChunk, len(icc))]
		p := make([]byte, 0, len(cICCHeader)+2+len(chunk))
		p = append(p, cICCHeader...)
		p = append(p, byte(n+1), byte(count))
		p = append(p, chunk...)
		segs = append(segs, p)
	}
	return segs
}

// withMetadata is the JPEG encoded (as Go writes one: SOI, then its
// tables) with exif (a TIFF structure, or nil) and icc (APP2 payloads)
// right after its SOI. An EXIF too large for its segment is left out.
func withMetadata(encoded, exif []byte, icc [][]byte) []byte {
	if len(encoded) < 2 || (exif == nil && len(icc) == 0) {
		return encoded
	}
	var out bytes.Buffer
	out.Grow(len(encoded) + len(exif) + 64*len(icc))
	out.Write(encoded[:2])
	segment := func(marker byte, parts ...[]byte) {
		n := 2
		for _, p := range parts {
			n += len(p)
		}
		out.Write([]byte{0xFF, marker, byte(n >> 8), byte(n)})
		for _, p := range parts {
			out.Write(p)
		}
	}
	if exif != nil && len(cExifHeader)+len(exif) <= cMaxSegment {
		segment(0xE1, []byte(cExifHeader), exif)
	}
	for _, p := range icc {
		if len(p) <= cMaxSegment {
			segment(0xE2, p)
		}
	}
	out.Write(encoded[2:])
	return out.Bytes()
}

// tiffView reads and writes a TIFF structure's numbers in its byte order.
type tiffView struct {
	b   []byte
	ord binary.ByteOrder
}

func newTIFF(b []byte) (*tiffView, error) {
	if len(b) < 8 {
		return nil, errors.New("EXIF too short") // i18n-ignore: an edit's internal error, logged
	}
	switch string(b[:4]) {
	case "II*\x00":
		return &tiffView{b, binary.LittleEndian}, nil
	case "MM\x00*":
		return &tiffView{b, binary.BigEndian}, nil
	}
	return nil, errors.New("EXIF without a TIFF header") // i18n-ignore: an edit's internal error, logged
}

func (t *tiffView) u16(at int) (int, bool) {
	if at < 0 || at+2 > len(t.b) {
		return 0, false
	}
	return int(t.ord.Uint16(t.b[at:])), true
}

func (t *tiffView) u32(at int) (int, bool) {
	if at < 0 || at+4 > len(t.b) {
		return 0, false
	}
	return int(t.ord.Uint32(t.b[at:])), true
}

// tiffEntry is one entry of an IFD: its tag, type, and where its 12 bytes
// start.
type tiffEntry struct {
	tag, typ, at int
}

// ifd is the entries of the IFD at off, and where its next-IFD offset is.
func (t *tiffView) ifd(off int) (entries []tiffEntry, next int, ok bool) {
	n, ok := t.u16(off)
	if !ok || n > 1000 || off+2+12*n+4 > len(t.b) {
		return nil, 0, false
	}
	for k := 0; k < n; k++ {
		at := off + 2 + 12*k
		tag, _ := t.u16(at)
		typ, _ := t.u16(at + 2)
		entries = append(entries, tiffEntry{tag: tag, typ: typ, at: at})
	}
	return entries, off + 2 + 12*n, true
}

// setNumber writes v as entry e's value: a SHORT or LONG held in the
// entry itself (count 1).
func (t *tiffView) setNumber(e tiffEntry, v int) bool {
	count, _ := t.u32(e.at + 4)
	if count != 1 {
		return false
	}
	switch e.typ {
	case 3: // SHORT
		if v < 0 || v > 0xFFFF {
			return false
		}
		t.ord.PutUint16(t.b[e.at+8:], uint16(v))
		t.ord.PutUint16(t.b[e.at+10:], 0)
	case 4: // LONG
		t.ord.PutUint32(t.b[e.at+8:], uint32(v))
	default:
		return false
	}
	return true
}

const (
	cTagOrientation = 0x0112
	cTagExifIFD     = 0x8769
	cTagPixelX      = 0xA002
	cTagPixelY      = 0xA003
)

// editedExif is a copy of exif (a TIFF structure) as it fits a photo
// edited to width x height, shown with orientation: that orientation, the
// Exif dimensions (when it has them) set to the edit's, and - dropThumb -
// without its embedded thumbnail (IFD1), which shows the photo before the
// edit. hasOrientation reports whether the orientation could be set: an
// EXIF without the tag in its first IFD can't take one. width or height 0
// leaves the dimensions alone.
func editedExif(exif []byte, orientation, width, height int, dropThumb bool) (out []byte, hasOrientation bool, err error) {
	out = bytes.Clone(exif)
	t, err := newTIFF(out)
	if err != nil {
		return nil, false, err
	}
	ifd0, _ := t.u32(4)
	entries, next, ok := t.ifd(ifd0)
	if !ok {
		return nil, false, errors.New("EXIF's first IFD is unreadable") // i18n-ignore: an edit's internal error, logged
	}
	for _, e := range entries {
		switch e.tag {
		case cTagOrientation:
			hasOrientation = t.setNumber(e, orientation)
		case cTagExifIFD:
			if width == 0 || height == 0 {
				continue
			}
			sub, ok := t.u32(e.at + 8)
			if !ok {
				continue
			}
			subEntries, _, ok := t.ifd(sub)
			if !ok {
				continue
			}
			for _, s := range subEntries {
				switch s.tag {
				case cTagPixelX:
					t.setNumber(s, width)
				case cTagPixelY:
					t.setNumber(s, height)
				}
			}
		}
	}
	if dropThumb {
		t.ord.PutUint32(t.b[next:], 0)
	}
	return out, hasOrientation, nil
}

// jpegWithExif is jpg (a JPEG with an EXIF APP1) with that EXIF replaced
// by exif, of the same length - its numbers changed in place - leaving
// every other byte as it was: a lossless turn.
func jpegWithExif(jpg, exif []byte) ([]byte, error) {
	segs, _, err := jpegHeaderSegments(jpg)
	if err != nil {
		return nil, err
	}
	for _, s := range segs {
		if s.marker == 0xE1 && bytes.HasPrefix(s.payload, []byte(cExifHeader)) {
			if len(s.payload)-len(cExifHeader) != len(exif) {
				return nil, errors.New("the EXIF changed length") // i18n-ignore: an edit's internal error, logged
			}
			out := bytes.Clone(jpg)
			copy(out[s.at+len(cExifHeader):], exif)
			return out, nil
		}
	}
	return nil, errors.New("no EXIF to change") // i18n-ignore: an edit's internal error, logged
}
