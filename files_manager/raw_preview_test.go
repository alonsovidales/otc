// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/image/tiff"
)

// tiffTag is one entry of a directory buildTIFF lays out: its values, or
// where blob (1-based) starts, or where the directories dirs start.
type tiffTag struct {
	tag, typ uint16
	vals     []uint32
	blob     int
	dirs     []int
}

// buildTIFF is a big-endian TIFF of the directories dirs - dirs[0] is
// IFD0, chained to dirs[next[0]] and so on (0 ends the chain) - followed
// by what they point at.
func buildTIFF(dirs [][]tiffTag, next []int, blobs [][]byte) []byte {
	bo := binary.BigEndian
	size := map[uint16]int{1: 1, 3: 2, 4: 4, 7: 1}
	// A directory's tags go in ascending order (x/image/tiff checks).
	for i := range dirs {
		dirs[i] = append([]tiffTag(nil), dirs[i]...)
		sort.Slice(dirs[i], func(a, b int) bool { return dirs[i][a].tag < dirs[i][b].tag })
	}
	dirAt := make([]uint32, len(dirs))
	at := uint32(8)
	for i, d := range dirs {
		dirAt[i] = at
		at += uint32(2 + 12*len(d) + 4)
	}
	// Values over 4 bytes, then the blobs.
	type extra struct {
		data []byte
		at   uint32
	}
	var extras []*extra
	values := func(t tiffTag) []uint32 {
		switch {
		case t.blob > 0:
			return nil // filled in below
		case t.dirs != nil:
			v := make([]uint32, len(t.dirs))
			for i, d := range t.dirs {
				v[i] = dirAt[d]
			}
			return v
		}
		return t.vals
	}
	encode := func(t tiffTag, v []uint32) []byte {
		var out []byte
		for _, x := range v {
			switch size[t.typ] {
			case 1:
				out = append(out, byte(x))
			case 2:
				out = bo.AppendUint16(out, uint16(x))
			case 4:
				out = bo.AppendUint32(out, x)
			}
		}
		return out
	}
	extraOf := map[[2]int]*extra{}
	for i, d := range dirs {
		for j, t := range d {
			if v := encode(t, values(t)); len(v) > 4 {
				e := &extra{data: v, at: at}
				at += uint32(len(v))
				extras = append(extras, e)
				extraOf[[2]int{i, j}] = e
			}
		}
	}
	blobAt := make([]uint32, len(blobs))
	for i, b := range blobs {
		blobAt[i] = at
		at += uint32(len(b))
	}

	out := append([]byte("MM\x00*"), bo.AppendUint32(nil, dirAt[0])...)
	for i, d := range dirs {
		out = bo.AppendUint16(out, uint16(len(d)))
		for j, t := range d {
			v := values(t)
			if t.blob > 0 {
				v = []uint32{blobAt[t.blob-1]}
			}
			out = bo.AppendUint16(out, t.tag)
			out = bo.AppendUint16(out, t.typ)
			out = bo.AppendUint32(out, uint32(len(v)))
			if e := extraOf[[2]int{i, j}]; e != nil {
				out = bo.AppendUint32(out, e.at)
			} else {
				out = append(out, append(encode(t, v), 0, 0, 0, 0)[:4]...)
			}
		}
		n := uint32(0)
		if next[i] != 0 {
			n = dirAt[next[i]]
		}
		out = bo.AppendUint32(out, n)
	}
	for _, e := range extras {
		out = append(out, e.data...)
	}
	for _, b := range blobs {
		out = append(out, b...)
	}
	return out
}

func short(tag uint16, v ...uint32) tiffTag { return tiffTag{tag: tag, typ: 3, vals: v} }
func long(tag uint16, v ...uint32) tiffTag  { return tiffTag{tag: tag, typ: 4, vals: v} }

// flatJPEG is a w x h JPEG of one colour.
func flatJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// assertFlat fails unless img is w x h and its centre is about c.
func assertFlat(t *testing.T, img image.Image, w, h int, c color.RGBA) {
	t.Helper()
	if got := img.Bounds(); got.Dx() != w || got.Dy() != h {
		t.Fatalf("decoded %v, want %dx%d", got, w, h)
	}
	r, g, b, _ := img.At(w/2, h/2).RGBA()
	got := [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
	for i, want := range []uint8{c.R, c.G, c.B} {
		if d := got[i] - int(want); d < -10 || d > 10 {
			t.Fatalf("centre is %v, want about %v", got, c)
		}
	}
}

var cRed, cGreen = color.RGBA{200, 40, 40, 255}, color.RGBA{40, 180, 60, 255}

// proRAWLike is shaped as an iPhone's ProRAW DNG (IMG_1361.DNG of
// raw.pixls.us): IFD0 the full-size preview, one strip of 8-bit YCbCr
// JPEG, with DNGVersion; its first sub-image the 12-bit linear raw.
func proRAWLike(t *testing.T) []byte {
	preview := flatJPEG(t, 64, 48, cRed)
	return buildTIFF([][]tiffTag{{
		long(tagSubfileType, 1), long(tagImageWidth, 64), long(tagImageLength, 48),
		short(tagBitsPerSample, 8, 8, 8), short(tagCompression, cCompressionJPEG), short(tagPhotometric, cPhotometricYCbCr),
		{tag: tagStripOffsets, typ: 4, blob: 1}, long(tagStripByteCounts, uint32(len(preview))),
		short(277, 3), {tag: tagSubIFDs, typ: 4, dirs: []int{1}}, {tag: tagDNGVersion, typ: 1, vals: []uint32{1, 4, 0, 0}},
	}, {
		long(tagSubfileType, 0), long(tagImageWidth, 4032), long(tagImageLength, 3024),
		short(tagBitsPerSample, 12, 12, 12), short(tagCompression, cCompressionJPEG), short(tagPhotometric, cPhotometricLinearRaw),
	}}, []int{0, 0}, [][]byte{preview})
}

// An iPhone's ProRAW photo said "tiff: unsupported feature: color model":
// x/image/tiff reads only IFD0 and has no YCbCr. Its preview is the photo.
func TestDecodeStillReadsProRAWPreview(t *testing.T) {
	dng := proRAWLike(t)
	if _, err := tiff.Decode(bytes.NewReader(dng)); err == nil || !strings.Contains(err.Error(), "color model") {
		t.Fatalf("x/image/tiff on it: %v, want the color model refusal the device reported", err)
	}
	if !isCameraRaw(dng) {
		t.Fatal("a DNG is a camera raw")
	}
	img, err := decodeStill(dng, "IMG_6410.DNG")
	if err != nil {
		t.Fatal(err)
	}
	assertFlat(t, img, 64, 48, cRed)
}

// Nikon's layout: IFD0 a tiny uncompressed thumbnail - which Go decodes -
// the preview an old-style JPEG in a sub-image, the sensor's mosaic in
// another. The preview, the larger, is the photo; not the thumbnail.
func TestDecodeStillPrefersARawsLargestPreview(t *testing.T) {
	preview := flatJPEG(t, 64, 48, cGreen)
	thumb := bytes.Repeat([]byte{40, 40, 200}, 8*6)
	nef := buildTIFF([][]tiffTag{{
		long(tagSubfileType, 1), long(tagImageWidth, 8), long(tagImageLength, 6),
		short(tagBitsPerSample, 8, 8, 8), short(tagCompression, 1), short(tagPhotometric, cPhotometricRGB),
		{tag: tagStripOffsets, typ: 4, blob: 2}, long(tagStripByteCounts, uint32(len(thumb))), short(277, 3), long(278, 6),
		{tag: tagSubIFDs, typ: 4, dirs: []int{1, 2}},
	}, {
		long(tagSubfileType, 1), long(tagImageWidth, 64), long(tagImageLength, 48),
		short(tagCompression, cCompressionOldJPEG), short(tagPhotometric, cPhotometricYCbCr),
		{tag: tagJPEGIF, typ: 4, blob: 1}, long(tagJPEGIFLength, uint32(len(preview))),
	}, {
		long(tagSubfileType, 0), long(tagImageWidth, 6048), long(tagImageLength, 4024),
		short(tagBitsPerSample, 14), short(tagCompression, 34713), short(tagPhotometric, cPhotometricCFA),
	}}, []int{0, 0, 0}, [][]byte{preview, thumb})

	if img, err := decodeImage(nef); err != nil || img.Bounds().Dx() != 8 {
		t.Fatalf("Go alone decodes the thumbnail: %v, %v", img, err)
	}
	img, err := decodeStill(nef, "DSC_0001.NEF")
	if err != nil {
		t.Fatal(err)
	}
	assertFlat(t, img, 64, 48, cGreen)
}

// splitJPEGTables moves a JPEG's quantization and Huffman tables into a
// stream of their own (SOI, tables, EOI), as a TIFF's JPEGTables keeps
// them, and returns that and the image without them.
func splitJPEGTables(t *testing.T, jpg []byte) (tables, rest []byte) {
	t.Helper()
	tables, rest = []byte{0xff, 0xd8}, []byte{0xff, 0xd8}
	i := 2
	for i+4 <= len(jpg) && jpg[i] == 0xff && jpg[i+1] != 0xda {
		n := int(binary.BigEndian.Uint16(jpg[i+2:])) + 2
		if jpg[i+1] == 0xdb || jpg[i+1] == 0xc4 {
			tables = append(tables, jpg[i:i+n]...)
		} else {
			rest = append(rest, jpg[i:i+n]...)
		}
		i += n
	}
	return append(tables, 0xff, 0xd9), append(rest, jpg[i:]...)
}

// A JPEG strip whose tables are the directory's JPEGTables gets them back.
func TestRawPreviewPutsBackJPEGTables(t *testing.T) {
	tables, strip := splitJPEGTables(t, flatJPEG(t, 64, 48, cRed))
	if _, err := jpeg.Decode(bytes.NewReader(strip)); err == nil {
		t.Fatal("the strip alone shouldn't decode: no tables were taken out")
	}
	tv := make([]uint32, len(tables))
	for i, b := range tables {
		tv[i] = uint32(b)
	}
	dng := buildTIFF([][]tiffTag{{
		long(tagImageWidth, 64), long(tagImageLength, 48), short(tagBitsPerSample, 8, 8, 8),
		short(tagCompression, cCompressionJPEG), short(tagPhotometric, cPhotometricYCbCr),
		{tag: tagStripOffsets, typ: 4, blob: 1}, long(tagStripByteCounts, uint32(len(strip))),
		{tag: tagJPEGTables, typ: 7, vals: tv}, {tag: tagDNGVersion, typ: 1, vals: []uint32{1, 4, 0, 0}},
	}}, []int{0}, [][]byte{strip})
	img, err := decodeStill(dng, "tables.dng")
	if err != nil {
		t.Fatal(err)
	}
	assertFlat(t, img, 64, 48, cRed)
}

// A damaged raw - cut anywhere - is an error, never a panic.
func TestRawPreviewOfADamagedFile(t *testing.T) {
	dng := proRAWLike(t)
	for n := 0; n < len(dng); n++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("cut to %d bytes: panicked: %v", n, r)
				}
			}()
			_, _ = rawPreview(dng[:n])
			_ = isCameraRaw(dng[:n])
		}()
	}
	// A directory that points at itself ends.
	loop := buildTIFF([][]tiffTag{{long(tagImageWidth, 1), {tag: tagSubIFDs, typ: 4, dirs: []int{0}}}}, []int{0}, nil)
	if _, err := rawPreview(loop); err == nil {
		t.Fatal("no preview in it")
	}
}

// OTC_TEST_RAW names a real camera raw (raw.pixls.us' Apple iPhone 12 Pro
// IMG_1361.DNG, ProRAW, 29 MB: not kept here) to decode as the device does.
func TestDecodeStillOfARealRaw(t *testing.T) {
	path := os.Getenv("OTC_TEST_RAW")
	if path == "" {
		t.Skip("OTC_TEST_RAW not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := decodeStill(b, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %v", path, img.Bounds())
	if img.Bounds().Dx() < 1000 {
		t.Fatalf("decoded %v: a thumbnail, not the preview", img.Bounds())
	}
}

// rawBomb is a little-endian TIFF of a region of 1 MiB, then dirs
// directories chained, each with every tag rawPreview reads, each a
// million BYTEs on that region - or, with preview, each directory a JPEG
// strip of the whole region, its first 4 KiB the JPEG tables to put back
// in.
func rawBomb(dirs int, preview bool) []byte {
	le := binary.LittleEndian
	const region, tables = 1 << 20, 4 << 10
	tags := []uint16{tagSubfileType, tagImageWidth, tagImageLength, tagBitsPerSample, tagCompression, tagPhotometric,
		tagStripOffsets, tagStripByteCounts, tagSubIFDs, tagJPEGTables, tagJPEGIF, tagJPEGIFLength, tagDNGVersion}
	b := make([]byte, 8+region)
	copy(b, "II*\x00")
	le.PutUint32(b[4:], uint32(len(b)))
	copy(b[8:], []byte{0xff, 0xd8})
	copy(b[8+tables-2:], []byte{0xff, 0xd9})
	one := map[uint16]uint32{tagCompression: cCompressionJPEG, tagPhotometric: cPhotometricYCbCr, tagBitsPerSample: 8,
		tagStripOffsets: 8, tagStripByteCounts: region, tagImageWidth: 1, tagImageLength: 1}
	for i := 0; i < dirs; i++ {
		at := uint32(len(b))
		entries := make([]byte, 2+len(tags)*12+4)
		le.PutUint16(entries, uint16(len(tags)))
		for j, tag := range tags {
			e := entries[2+j*12:]
			le.PutUint16(e, tag)
			le.PutUint16(e[2:], 1)     // BYTE
			le.PutUint32(e[4:], 1<<20) // a million of them
			le.PutUint32(e[8:], 8)     // all on the region
			if v, ok := one[tag]; ok && preview {
				le.PutUint16(e[2:], 4) // one LONG
				le.PutUint32(e[4:], 1)
				le.PutUint32(e[8:], v)
			}
			if tag == tagJPEGTables && preview {
				le.PutUint32(e[4:], tables)
			}
		}
		next := uint32(0)
		if i < dirs-1 {
			next = at + uint32(len(entries))
		}
		le.PutUint32(entries[len(entries)-4:], next)
		b = append(b, entries...)
	}
	return b
}

// A crafted camera raw of a megabyte - 64 directories of every tag the
// parser reads, each a million values on the same bytes - held 3.4 GB
// before the values were capped (maxValues, cMaxRawValues): it is read
// within a few MiB, and a preview put together once, not once for each
// directory that has one.
func TestCraftedRawIsCheapToRead(t *testing.T) {
	for _, c := range []struct {
		name    string
		preview bool
	}{{"every tag a million values", false}, {"64 previews with tables", true}} {
		b := rawBomb(cMaxRawIFDs, c.preview)
		var ifds []tiffIFD
		var jpg []byte
		var err error
		n := allocatedBy(func() {
			ifds, _ = tiffIFDs(b)
			_ = isCameraRaw(b)
			jpg, err = rawPreview(b)
		})
		if len(ifds) != cMaxRawIFDs {
			t.Fatalf("%s: %d directories read", c.name, len(ifds))
		}
		if c.preview && (err != nil || len(jpg) != 1<<20+4<<10-4) {
			t.Fatalf("%s: a preview of %d bytes, %v", c.name, len(jpg), err)
		}
		if n > 12<<20 {
			t.Errorf("%s: %d MiB allocated for a %d KiB file", c.name, n>>20, len(b)>>10)
		}
		for _, d := range ifds {
			for tag, v := range d {
				if uint64(len(v)) > maxValues(tag) {
					t.Fatalf("%s: tag %d kept %d values", c.name, tag, len(v))
				}
			}
		}
	}
}

// allocatedBy is what f allocates, in bytes.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
