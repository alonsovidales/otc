// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// A camera raw - a DNG (Apple ProRAW among them), and the TIFF-shaped raws
// of Nikon, Sony and Canon - holds its sensor data in a form no decoder
// here reads (x/image/tiff: "unsupported feature: color model" for
// ProRAW's linear raw, or a JPEG-compressed image it can't decompress),
// beside a preview the camera made: an 8-bit JPEG, full size in an
// iPhone's DNG. rawPreview finds the largest one, which is what the device
// makes its thumbnail from, tags and searches for faces.

// errNoRawPreview: no JPEG preview in the file (or not a TIFF at all).
var errNoRawPreview = errors.New("no JPEG preview in this camera raw") // i18n-ignore: a sentinel the callers catch, never shown

const (
	tagSubfileType     = 254
	tagImageWidth      = 256
	tagImageLength     = 257
	tagBitsPerSample   = 258
	tagCompression     = 259
	tagPhotometric     = 262
	tagStripOffsets    = 273
	tagStripByteCounts = 279
	tagSubIFDs         = 330
	tagJPEGTables      = 347
	tagJPEGIF          = 513
	tagJPEGIFLength    = 514
	tagDNGVersion      = 50706

	cCompressionOldJPEG = 6
	cCompressionJPEG    = 7

	cPhotometricGray      = 1
	cPhotometricRGB       = 2
	cPhotometricYCbCr     = 6
	cPhotometricCFA       = 32803
	cPhotometricLinearRaw = 34892

	// Enough for any camera's IFD0 chain and sub-images; a loop in a
	// damaged file ends here.
	cMaxRawIFDs = 64

	// cMaxJPEGTables is the most of a directory's JPEG tables read: a
	// JPEG's quantization and Huffman tables take a few hundred bytes.
	cMaxJPEGTables = 64 << 10

	// cMaxRawValues is the most values tiffIFDs reads in all, every
	// directory's together: a camera's raw holds a few dozen.
	cMaxRawValues = 1 << 18
)

// What a crafted TIFF of a megabyte held before the values were capped:
// each of a directory's 13 tags up to a million values (4 MiB), 64
// directories, every tag pointing at the same bytes - 3.4 GB, read before
// the decode budget is asked for anything (a header that says 1 x 1
// costs nothing to decode). maxValues is how many of a tag's values
// rawPreview ever looks at.
func maxValues(tag uint16) uint64 {
	switch tag {
	case tagBitsPerSample:
		return 16 // a sample's bits, for each of a pixel's samples
	case tagStripOffsets, tagStripByteCounts:
		return 2 // one strip, or more: not a preview
	case tagSubIFDs:
		return cMaxRawIFDs
	case tagJPEGTables:
		return cMaxJPEGTables
	case tagDNGVersion:
		return 4
	}
	return 1 // the first value only (tiffIFD.first)
}

// tiffIFD is the tags of one image directory rawPreview reads.
type tiffIFD map[uint16][]uint32

func (d tiffIFD) first(tag uint16) uint32 {
	if v := d[tag]; len(v) > 0 {
		return v[0]
	}
	return 0
}

// isTIFF reports whether b starts with a TIFF header.
func isTIFF(b []byte) bool {
	return len(b) >= 8 && (string(b[:4]) == "II*\x00" || string(b[:4]) == "MM\x00*")
}

// isCameraRaw reports whether b is a TIFF holding a camera's raw: a DNG,
// or a directory of sensor data (a colour filter mosaic, linear raw).
func isCameraRaw(b []byte) bool {
	ifds, err := tiffIFDs(b)
	if err != nil {
		return false
	}
	if len(ifds[0][tagDNGVersion]) > 0 {
		return true
	}
	for _, d := range ifds {
		if p := d.first(tagPhotometric); p == cPhotometricCFA || p == cPhotometricLinearRaw {
			return true
		}
	}
	return false
}

// rawPreview is the largest 8-bit JPEG image in the TIFF b: its IFD0
// chain and their sub-images (tag 330), each one strip of JPEG
// (compression 7, with the directory's JPEG tables put back in) or an
// old-style JPEG (compression 6, at its JPEGInterchangeFormat offset).
// Only the largest is put together - a strip with its tables is a copy.
func rawPreview(b []byte) ([]byte, error) {
	ifds, err := tiffIFDs(b)
	if err != nil {
		return nil, err
	}
	var best, bestTables []byte
	var bestPixels uint64
	found := false
	for _, d := range ifds {
		jpg, tables, ok := previewOf(b, d)
		if !ok {
			continue
		}
		if px := uint64(d.first(tagImageWidth)) * uint64(d.first(tagImageLength)); !found || px > bestPixels {
			best, bestTables, bestPixels, found = jpg, tables, px, true
		}
	}
	if !found {
		return nil, errNoRawPreview
	}
	return joinedJPEG(best, bestTables), nil
}

// previewOf is d's image when it is an 8-bit JPEG one: its JPEG stream's
// bytes in b, and the tables kept apart from it that go back in (nil when
// there are none); ok false otherwise.
func previewOf(b []byte, d tiffIFD) (jpg, tables []byte, ok bool) {
	switch d.first(tagPhotometric) {
	case cPhotometricYCbCr, cPhotometricRGB, cPhotometricGray:
	default:
		return nil, nil, false // linear raw, a sensor's mosaic, Apple's depth map...
	}
	for _, bits := range d[tagBitsPerSample] {
		if bits != 8 {
			return nil, nil, false
		}
	}
	slice := func(off, n uint32) []byte {
		if n < 4 || uint64(off)+uint64(n) > uint64(len(b)) {
			return nil
		}
		return b[off : off+n]
	}
	switch d.first(tagCompression) {
	case cCompressionJPEG:
		offs, counts := d[tagStripOffsets], d[tagStripByteCounts]
		if len(offs) != 1 || len(counts) != 1 {
			return nil, nil, false // tiled or in strips: not a preview
		}
		jpg = slice(offs[0], counts[0])
		// The tables may be kept apart, once for every strip: SOI, the
		// tables, EOI, which go back between the strip's SOI and the rest.
		if t := d[tagJPEGTables]; jpg != nil && len(t) > 4 {
			tb := make([]byte, len(t))
			for i, v := range t {
				tb[i] = byte(v)
			}
			if bytes.HasPrefix(tb, []byte{0xff, 0xd8}) && bytes.HasSuffix(tb, []byte{0xff, 0xd9}) {
				return jpg, tb, true
			}
		}
	case cCompressionOldJPEG:
		jpg = slice(d.first(tagJPEGIF), d.first(tagJPEGIFLength))
	}
	if !bytes.HasPrefix(jpg, []byte{0xff, 0xd8}) {
		return nil, nil, false
	}
	return jpg, nil, true
}

// joinedJPEG is the JPEG stream previewOf's parts make: jpg itself, or
// with tables put back between its SOI and the rest.
func joinedJPEG(jpg, tables []byte) []byte {
	if tables == nil {
		return jpg
	}
	return append(append([]byte{}, tables[:len(tables)-2]...), jpg[2:]...)
}

// tiffIFDs reads b's IFD0 chain and every sub-image directory, each once.
func tiffIFDs(b []byte) ([]tiffIFD, error) {
	if !isTIFF(b) {
		return nil, errNoRawPreview
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if b[0] == 'M' {
		bo = binary.BigEndian
	}
	var out []tiffIFD
	seen := map[uint32]bool{}
	queue := []uint32{bo.Uint32(b[4:8])}
	left := uint64(cMaxRawValues)
	for len(queue) > 0 && len(out) < cMaxRawIFDs {
		off := queue[0]
		queue = queue[1:]
		if off == 0 || seen[off] {
			continue
		}
		seen[off] = true
		d, next, ok := readIFD(b, bo, off, &left)
		if !ok {
			continue
		}
		out = append(out, d)
		queue = append(append(queue, next), d[tagSubIFDs]...)
	}
	if len(out) == 0 {
		return nil, errNoRawPreview
	}
	return out, nil
}

// readIFD reads the directory at off: the tags rawPreview uses - as many
// values of each as it looks at (maxValues), out of the left the whole
// parse may still read - and the next directory's offset.
func readIFD(b []byte, bo binary.ByteOrder, off uint32, left *uint64) (d tiffIFD, next uint32, ok bool) {
	if uint64(off)+2 > uint64(len(b)) {
		return nil, 0, false
	}
	n := uint64(bo.Uint16(b[off:]))
	end := uint64(off) + 2 + n*12
	if end+4 > uint64(len(b)) {
		return nil, 0, false
	}
	d = tiffIFD{}
	for i := uint64(0); i < n; i++ {
		e := b[uint64(off)+2+i*12:]
		tag, typ, count := bo.Uint16(e), bo.Uint16(e[2:]), uint64(bo.Uint32(e[4:]))
		switch tag {
		case tagSubfileType, tagImageWidth, tagImageLength, tagBitsPerSample, tagCompression, tagPhotometric,
			tagStripOffsets, tagStripByteCounts, tagSubIFDs, tagJPEGTables, tagJPEGIF, tagJPEGIFLength, tagDNGVersion:
		default:
			continue
		}
		var size uint64
		switch typ {
		case 1, 7: // BYTE, UNDEFINED
			size = 1
		case 3: // SHORT
			size = 2
		case 4, 13: // LONG, IFD
			size = 4
		}
		if size == 0 || count == 0 || count > 1<<20 {
			continue
		}
		data := e[8:12]
		if count*size > 4 {
			at := uint64(bo.Uint32(e[8:]))
			if at+count*size > uint64(len(b)) {
				continue
			}
			data = b[at : at+count*size]
		}
		count = min(count, maxValues(tag))
		if count > *left {
			continue
		}
		*left -= count
		vals := make([]uint32, count)
		for j := range vals {
			switch size {
			case 1:
				vals[j] = uint32(data[j])
			case 2:
				vals[j] = uint32(bo.Uint16(data[2*j:]))
			case 4:
				vals[j] = bo.Uint32(data[4*j:])
			}
		}
		d[tag] = vals
	}
	return d, bo.Uint32(b[end:]), true
}
