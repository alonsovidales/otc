// SPDX-License-Identifier: AGPL-3.0-or-later

package exifinfo

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"slices"

	"github.com/rwcarlsen/goexif/exif"
)

// The EXIF parser (goexif) trusts the TIFF structure EXIF is kept in. It
// works out the size of a tag's value as its count times its type's size
// in 32 bits, and then makes room for count values: a tag of a few bytes
// asked for 4 to 16 GiB, an out-of-memory death of the whole process that
// no recover() catches. It follows the chain of image directories until
// one says it is the last - two that name each other never do - and it
// decodes every tag of every directory, however many of them point at the
// same bytes.
//
// So goexif is never given a file's own EXIF. compactTIFF reads the few
// tags fromExif uses, with 64-bit bounds and a few hundred KiB at most,
// from the directories goexif takes them from - IFD0, the Exif sub-IFD
// and the GPS one, chosen as goexif chooses them - and writes them into a
// small TIFF of their own, which is what goexif decodes. A TIFF (a camera
// raw, a scan) is read only where those tags are, never whole. A tag or a
// directory that is malformed is left out on its own, where goexif gave up
// on the whole file.

var (
	errNoExifSegment = errors.New("exif: no EXIF segment")
	errNotTIFF       = errors.New("exif: not a TIFF structure")
	errNoIFD0        = errors.New("exif: no first image directory")
)

const (
	// cMaxKeptValue is the most of a tag's value compactTIFF keeps, in
	// whole values: far more than a camera's name or a date takes.
	cMaxKeptValue = 4 << 10

	// cMaxKeptRepeats is how many tags of one offset id are kept, the last
	// ones: offsetTags reads every one of them, in order.
	cMaxKeptRepeats = 8

	tagExifIFD = 0x8769
	tagGPSIFD  = 0x8825
)

// keptMain is every tag fromExif and takenAt read by goexif's names from
// IFD0 and the Exif sub-IFD, which goexif files in one table (a later tag
// replacing an earlier one of the same id). A tag fromExif starts to read
// has to be added here.
var keptMain = map[uint16]bool{
	0x010F: true, // Make
	0x0110: true, // Model
	0x0112: true, // Orientation
	0x0132: true, // DateTime
	0x829A: true, // ExposureTime
	0x829D: true, // FNumber
	0x8827: true, // ISOSpeedRatings
	0x9003: true, // DateTimeOriginal
	0x9004: true, // DateTimeDigitized
	0x920A: true, // FocalLength
	0xA002: true, // PixelXDimension
	0xA003: true, // PixelYDimension
}

// keptGPS is the GPS sub-IFD's tags fromExif and gpsOffset read.
var keptGPS = map[uint16]bool{
	0x0001: true, // GPSLatitudeRef
	0x0002: true, // GPSLatitude
	0x0003: true, // GPSLongitudeRef
	0x0004: true, // GPSLongitude
	0x0007: true, // GPSTimeStamp
	0x001D: true, // GPSDateStamp
}

// keptOffsets is the tags offsetTags reads from the directory goexif's
// table names as the Exif sub-IFD (taken.go).
var keptOffsets = map[uint16]bool{tagOffsetTime: true, tagOffsetTimeOriginal: true, tagOffsetTimeDigitized: true}

// typeSizes is the bytes a value of each TIFF type takes, the types goexif
// knows (1 BYTE to 12 DOUBLE). A tag of any other type made goexif refuse
// the whole file; here it is left out.
var typeSizes = [13]uint64{0, 1, 1, 2, 4, 8, 1, 1, 2, 4, 8, 4, 8}

// tiffEntry is a directory entry compactTIFF keeps, its value as stored
// (cut to cMaxKeptValue: its count says how many values are left).
type tiffEntry struct {
	id, typ uint16
	count   uint32
	value   []byte
}

// byteOrder is a TIFF's byte order, read and written.
type byteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// tiffReader reads the TIFF structure at r, size bytes of it.
type tiffReader struct {
	r     io.ReaderAt
	size  int64
	order byteOrder
}

// read is the n bytes at off; false when they aren't all there.
func (t *tiffReader) read(off, n uint64) ([]byte, bool) {
	if n > uint64(t.size) || off > uint64(t.size)-n {
		return nil, false
	}
	b := make([]byte, n)
	got, _ := t.r.ReadAt(b, int64(off))
	return b, got == len(b)
}

// dir is the entries of the directory at off, 12 bytes each: as many as
// its count says - a signed 16 bits for goexif, so at most 32767, and none
// when it reads as negative - or the ones that are there when it is cut
// short.
func (t *tiffReader) dir(off uint64) ([]byte, bool) {
	head, ok := t.read(off, 2)
	if !ok {
		return nil, false
	}
	n := int16(t.order.Uint16(head))
	if n <= 0 {
		return nil, true
	}
	room := (uint64(t.size) - off - 2) / 12
	return t.read(off+2, min(uint64(n), room)*12)
}

// entry reads the entry e (12 bytes) and its value, whole values of it up
// to cMaxKeptValue. False for a type or count goexif refuses and for a
// value that isn't all in the data - the size worked out in 64 bits.
func (t *tiffReader) entry(e []byte) (tiffEntry, bool) {
	id, typ, count := t.order.Uint16(e), t.order.Uint16(e[2:]), t.order.Uint32(e[4:])
	if int(typ) >= len(typeSizes) || typeSizes[typ] == 0 || count == 0 || count == math.MaxUint32 {
		return tiffEntry{}, false
	}
	size := typeSizes[typ]
	need := size * uint64(count)
	keep := min(need, cMaxKeptValue/size*size)
	var value []byte
	if need <= 4 {
		value = slices.Clone(e[8 : 8+need])
	} else {
		at := uint64(t.order.Uint32(e[8:]))
		if at > uint64(t.size) || need > uint64(t.size)-at {
			return tiffEntry{}, false
		}
		v, ok := t.read(at, keep)
		if !ok {
			return tiffEntry{}, false
		}
		value = v
	}
	return tiffEntry{id: id, typ: typ, count: uint32(keep / size), value: value}, true
}

// pointer is the offset a sub-IFD pointer names: its first value, as
// goexif reads it (Int64(0): only an integer type, the signed ones
// sign-extended). False for any other type and for an offset outside the
// data.
func (t *tiffReader) pointer(e tiffEntry) (uint64, bool) {
	var v int64
	switch e.typ {
	case 1:
		v = int64(e.value[0])
	case 3:
		v = int64(t.order.Uint16(e.value))
	case 4:
		v = int64(t.order.Uint32(e.value))
	case 6:
		v = int64(int8(e.value[0]))
	case 8:
		v = int64(int16(t.order.Uint16(e.value)))
	case 9:
		v = int64(int32(t.order.Uint32(e.value)))
	default:
		return 0, false
	}
	if v <= 0 || v >= t.size {
		return 0, false
	}
	return uint64(v), true
}

// subIFDs is where the pointers goexif's table holds lead, as directories
// are filed in it.
type subIFDs struct {
	exif, gps       tiffEntry
	hasExif, hasGPS bool
}

// at is the offset of a sub-IFD the table names (ok false: none).
func (t *tiffReader) at(e tiffEntry, has bool) (uint64, bool) {
	if !has {
		return 0, false
	}
	return t.pointer(e)
}

// last is the entries of raw (a directory's) with an id kept holds, at
// most n of each - the last ones, as a later tag replaces an earlier one
// in goexif's table - in order, their values read. Only those values are
// read: a directory of thousands of copies of one tag costs what n do.
func (t *tiffReader) last(raw []byte, kept func(id uint16) bool, n int) []tiffEntry {
	seen := map[uint16]int{}
	var at []int
	for i := len(raw)/12*12 - 12; i >= 0; i -= 12 {
		if id := t.order.Uint16(raw[i:]); kept(id) && seen[id] < n {
			seen[id]++
			at = append(at, i)
		}
	}
	slices.Reverse(at)
	var out []tiffEntry
	for _, i := range at {
		if e, ok := t.entry(raw[i : i+12]); ok {
			out = append(out, e)
		}
	}
	return out
}

// load files a directory's entries as goexif files IFD0's and the Exif
// sub-IFD's: the kept tags into main and the pointers into subs, the last
// of an id winning.
func (t *tiffReader) load(raw []byte, main map[uint16]tiffEntry, subs *subIFDs) {
	filed := func(id uint16) bool { return keptMain[id] || id == tagExifIFD || id == tagGPSIFD }
	for _, e := range t.last(raw, filed, 1) {
		switch e.id {
		case tagExifIFD:
			subs.exif, subs.hasExif = e, true
		case tagGPSIFD:
			subs.gps, subs.hasGPS = e, true
		default:
			main[e.id] = e
		}
	}
}

// compactTIFF reads the TIFF structure at r (size bytes) and returns a
// TIFF of its own holding what fromExif reads of it: IFD0's and the Exif
// sub-IFD's kept tags (the last of each id), in its IFD0; the offset tags
// of the directory goexif's table ends up naming as the Exif sub-IFD, in
// order, in its Exif sub-IFD; the kept GPS tags (the last of each), in its
// GPS sub-IFD.
func compactTIFF(r io.ReaderAt, size int64) ([]byte, error) {
	t := &tiffReader{r: r, size: size}
	head, ok := t.read(0, 8)
	if !ok {
		return nil, errNotTIFF
	}
	switch string(head[:4]) {
	case "II*\x00":
		t.order = binary.LittleEndian
	case "MM\x00*":
		t.order = binary.BigEndian
	default:
		return nil, errNotTIFF
	}
	ifd0 := uint64(t.order.Uint32(head[4:]))
	if ifd0 == 0 {
		return nil, errNoIFD0
	}
	raw0, ok := t.dir(ifd0)
	if !ok {
		return nil, errNoIFD0
	}
	main := map[uint16]tiffEntry{}
	var subs subIFDs
	t.load(raw0, main, &subs)

	var exifRaw []byte
	exifAt, hasExif := t.at(subs.exif, subs.hasExif)
	if hasExif {
		if raw, ok := t.dir(exifAt); ok {
			exifRaw = raw
			t.load(raw, main, &subs)
		}
	}
	// The Exif sub-IFD may name another one, which is the table's last
	// word on where it is: the one offsetTags reads.
	var offsets []tiffEntry
	if at, ok := t.at(subs.exif, subs.hasExif); ok {
		raw := exifRaw
		if !hasExif || at != exifAt {
			raw, _ = t.dir(at)
		}
		offsets = t.last(raw, func(id uint16) bool { return keptOffsets[id] }, cMaxKeptRepeats)
	}
	gps := map[uint16]tiffEntry{}
	if at, ok := t.at(subs.gps, subs.hasGPS); ok {
		if raw, ok := t.dir(at); ok {
			for _, e := range t.last(raw, func(id uint16) bool { return keptGPS[id] }, 1) {
				gps[e.id] = e
			}
		}
	}
	return writeTIFF(t.order, sortedEntries(main), offsets, sortedEntries(gps)), nil
}

// sortedEntries is m's entries by id.
func sortedEntries(m map[uint16]tiffEntry) []tiffEntry {
	out := make([]tiffEntry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b tiffEntry) int { return int(a.id) - int(b.id) })
	return out
}

// dirSize is the bytes a directory of n entries takes, and valuesSize what
// the values over four bytes of its entries take after it (each padded to
// an even length, as TIFF wants).
func dirSize(n int) uint32 { return uint32(2 + 12*n + 4) }

func valuesSize(entries []tiffEntry) uint32 {
	var n uint32
	for _, e := range entries {
		if len(e.value) > 4 {
			n += uint32(len(e.value) + len(e.value)%2)
		}
	}
	return n
}

// pointerEntry is a LONG entry holding a sub-IFD's offset.
func pointerEntry(order byteOrder, id uint16, at uint32) tiffEntry {
	return tiffEntry{id: id, typ: 4, count: 1, value: order.AppendUint32(nil, at)}
}

// writeTIFF lays out a TIFF in order: the header, IFD0 (ifd0, and a
// pointer to each sub-IFD that has entries), the Exif sub-IFD, the GPS
// one - each directory followed by its values.
func writeTIFF(order byteOrder, ifd0, exifDir, gpsDir []tiffEntry) []byte {
	n0 := len(ifd0)
	if len(exifDir) > 0 {
		n0++
	}
	if len(gpsDir) > 0 {
		n0++
	}
	const at0 = 8
	atExif := at0 + dirSize(n0) + valuesSize(ifd0)
	atGPS := atExif
	if len(exifDir) > 0 {
		atGPS += dirSize(len(exifDir)) + valuesSize(exifDir)
	}
	dir0 := slices.Clone(ifd0)
	if len(exifDir) > 0 {
		dir0 = append(dir0, pointerEntry(order, tagExifIFD, atExif))
	}
	if len(gpsDir) > 0 {
		dir0 = append(dir0, pointerEntry(order, tagGPSIFD, atGPS))
	}
	slices.SortStableFunc(dir0, func(a, b tiffEntry) int { return int(a.id) - int(b.id) })

	var b bytes.Buffer
	if order == byteOrder(binary.LittleEndian) {
		b.WriteString("II*\x00")
	} else {
		b.WriteString("MM\x00*")
	}
	b.Write(order.AppendUint32(nil, at0))
	writeTIFFDir(&b, order, dir0, at0)
	if len(exifDir) > 0 {
		writeTIFFDir(&b, order, exifDir, atExif)
	}
	if len(gpsDir) > 0 {
		writeTIFFDir(&b, order, gpsDir, atGPS)
	}
	return b.Bytes()
}

// writeTIFFDir writes entries as the directory at offset at (no next one),
// their values over four bytes right after it.
func writeTIFFDir(b *bytes.Buffer, order byteOrder, entries []tiffEntry, at uint32) {
	b.Write(order.AppendUint16(nil, uint16(len(entries))))
	next := at + dirSize(len(entries))
	var values []byte
	for _, e := range entries {
		b.Write(order.AppendUint16(nil, e.id))
		b.Write(order.AppendUint16(nil, e.typ))
		b.Write(order.AppendUint32(nil, e.count))
		if len(e.value) <= 4 {
			field := make([]byte, 4)
			copy(field, e.value)
			b.Write(field)
			continue
		}
		b.Write(order.AppendUint32(nil, next+uint32(len(values))))
		values = append(values, e.value...)
		if len(e.value)%2 == 1 {
			values = append(values, 0)
		}
	}
	b.Write(order.AppendUint32(nil, 0))
	b.Write(values)
}

// decodeTIFF is what goexif reads of the EXIF in the TIFF structure at r
// (size bytes), through compactTIFF.
func decodeTIFF(r io.ReaderAt, size int64) (*Info, error) {
	compact, err := compactTIFF(r, size)
	if err != nil {
		return nil, err
	}
	x, err := exif.Decode(bytes.NewReader(compact))
	if err != nil {
		return nil, err
	}
	return fromExif(x), nil
}

// fromExifData reads the EXIF of what is at r (size bytes, from its
// start), told apart by its first bytes as goexif tells it: a TIFF; a
// block that starts "Exif\0\0" (a HEIC's Exif item); anything else as a
// JPEG, whose EXIF is in an APP1 segment (exifSegment).
func fromExifData(r io.ReaderAt, size int64) (*Info, error) {
	head := make([]byte, 6)
	n, _ := r.ReadAt(head, 0)
	switch {
	case n >= 4 && (string(head[:4]) == "II*\x00" || string(head[:4]) == "MM\x00*"):
		return decodeTIFF(r, size)
	case n >= 4 && string(head[:4]) == "Exif":
		if n < 6 || string(head) != "Exif\x00\x00" {
			return nil, errNoExifSegment
		}
		return decodeTIFF(io.NewSectionReader(r, 6, size-6), size-6)
	}
	tiff, err := exifSegment(r, size)
	if err != nil {
		return nil, err
	}
	return decodeTIFF(bytes.NewReader(tiff), int64(len(tiff)))
}

// exifSegment is the TIFF structure of a JPEG's EXIF: the payload of its
// first APP1 segment that starts "Exif\0\0", without those six bytes. The
// markers are followed from the start of a JPEG for as long as they make
// sense - past segments of other kinds, XMP's own APP1 among them - and
// then, or for data that isn't a JPEG (a camera raw with one inside), the
// segment is looked for byte by byte, as goexif does. Nothing but one
// segment (64 KiB at most) is held.
func exifSegment(r io.ReaderAt, size int64) ([]byte, error) {
	in := bufio.NewReaderSize(io.NewSectionReader(r, 0, size), 64<<10)
	if soi, err := in.Peek(2); err == nil && soi[0] == 0xFF && soi[1] == 0xD8 {
		in.Discard(2)
		if tiff, done, err := walkSegments(in); done {
			return tiff, err
		}
	}
	for {
		if _, err := in.ReadSlice(0xFF); err == bufio.ErrBufferFull {
			continue
		} else if err != nil {
			return nil, errNoExifSegment
		}
		code, err := markerCode(in)
		if err != nil {
			return nil, errNoExifSegment
		}
		if code != 0xE1 {
			continue
		}
		n, err := segmentLength(in)
		if err != nil {
			return nil, errNoExifSegment
		}
		if tiff, ok, err := exifPayload(in, n); err != nil || ok {
			return tiff, err
		}
	}
}

// walkSegments follows a JPEG's markers from just after its SOI. done is
// true when it found the EXIF (or the data ended inside a segment);
// false when it reached the image data, the image's end or something that
// isn't a marker - exifSegment looks on from there.
func walkSegments(in *bufio.Reader) (tiff []byte, done bool, err error) {
	for {
		if c, err := in.ReadByte(); err != nil {
			return nil, true, errNoExifSegment
		} else if c != 0xFF {
			return nil, false, nil
		}
		code, err := markerCode(in)
		if err != nil {
			return nil, true, errNoExifSegment
		}
		switch {
		case code == 0xD8 || code == 0x01 || (code >= 0xD0 && code <= 0xD7):
			continue // a marker without a segment
		case code == 0xDA || code == 0xD9:
			return nil, false, nil // the image data, or its end
		}
		n, err := segmentLength(in)
		if err != nil {
			return nil, true, errNoExifSegment
		}
		if code == 0xE1 {
			if tiff, ok, err := exifPayload(in, n); err != nil || ok {
				return tiff, true, err
			}
		}
		if _, err := in.Discard(n); err != nil {
			return nil, true, errNoExifSegment
		}
	}
}

// markerCode is the code of the marker whose 0xFF was just read, past any
// fill bytes (more 0xFF).
func markerCode(in *bufio.Reader) (byte, error) {
	for {
		c, err := in.ReadByte()
		if err != nil || c != 0xFF {
			return c, err
		}
	}
}

// segmentLength reads a segment's length and is what its payload takes
// (the length counts its own two bytes; 0 for one that says less).
func segmentLength(in *bufio.Reader) (int, error) {
	var l [2]byte
	if _, err := io.ReadFull(in, l[:]); err != nil {
		return 0, err
	}
	return max(int(binary.BigEndian.Uint16(l[:]))-2, 0), nil
}

// exifPayload reads the APP1 payload of n bytes at in when it is EXIF's:
// its TIFF structure, ok true. Any other payload is left unread.
func exifPayload(in *bufio.Reader, n int) (tiff []byte, ok bool, err error) {
	if n < 6 {
		return nil, false, nil
	}
	if head, err := in.Peek(6); err != nil || string(head) != "Exif\x00\x00" {
		return nil, false, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(in, payload); err != nil {
		return nil, false, errNoExifSegment
	}
	return payload[6:], true, nil
}
