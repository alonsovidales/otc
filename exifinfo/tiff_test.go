// SPDX-License-Identifier: AGPL-3.0-or-later

package exifinfo

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

// rawEntry is a directory entry written as it is, whatever it says.
type rawEntry struct {
	id, typ      uint16
	count, value uint32
}

// rawDir is a little-endian directory of entries, then next (the next
// directory's offset, 0 for none).
func rawDir(entries []rawEntry, next uint32) []byte {
	le := binary.LittleEndian
	b := le.AppendUint16(nil, uint16(len(entries)))
	for _, e := range entries {
		b = le.AppendUint16(b, e.id)
		b = le.AppendUint16(b, e.typ)
		b = le.AppendUint32(b, e.count)
		b = le.AppendUint32(b, e.value)
	}
	return le.AppendUint32(b, next)
}

// tiffWith is a little-endian TIFF: its header, the directory of
// entries at 8 (then next), and data after it - at 8+len(dir).
func tiffWith(entries []rawEntry, next uint32, data []byte) []byte {
	b := append([]byte("II*\x00"), 8, 0, 0, 0)
	b = append(b, rawDir(entries, next)...)
	return append(b, data...)
}

// dataAt is where tiffWith puts its data for n entries.
func dataAt(n int) uint32 { return uint32(8 + 2 + 12*n + 4) }

// inJPEG wraps a TIFF in a JPEG's EXIF segment.
func inJPEG(tiff []byte) []byte {
	payload := append([]byte("Exif\x00\x00"), tiff...)
	if len(payload)+2 > 0xFFFF {
		panic("an EXIF segment holds at most 64 KiB")
	}
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(payload)+2))
	out = append(out, payload...)
	return append(out, 0xFF, 0xD9)
}

// crafted are EXIF blocks whose structure lies about itself, as TIFFs.
// goexif - before compactTIFF stood in front of it - allocated 4 to 16
// GiB for the first ones (a count whose size overflowed 32 bits), decoded
// thousands of tags from the same bytes, and never came back from a loop
// of directories.
func crafted() map[string][]byte {
	cases := map[string][]byte{}
	// Every type goexif converts, with a count whose size wraps to a few
	// bytes in 32 bits: the 8 bytes after the directory are all there is.
	for name, e := range map[string]rawEntry{
		"double count past 32 bits":   {0x010F, 12, 0x20000001, dataAt(1)},
		"short count past 32 bits":    {0x0112, 3, 0x80000001, dataAt(1)},
		"rational count past 32 bits": {0x829A, 5, 0x20000001, dataAt(1)},
		"long count past 32 bits":     {0x0100, 4, 0x40000001, dataAt(1)},
		"slong count past 32 bits":    {0x8769, 9, 0x40000001, dataAt(1)},
		"count of MaxUint32":          {0x0110, 2, 0xFFFFFFFF, dataAt(1)},
	} {
		cases[name] = tiffWith([]rawEntry{e}, 0, make([]byte, 8))
	}
	// 4000 tags of 16 KiB each - camera makes and EXIF versions, one
	// read here and one not - all the same 16 KiB: 64 MiB of values, 512
	// MiB once goexif made each byte an int64, in a 64 KiB segment.
	var many []rawEntry
	for i := 0; i < 4000; i++ {
		many = append(many, rawEntry{[]uint16{0x010F, 0x9000}[i%2], 1, 16 << 10, dataAt(4000)})
	}
	cases["many tags on the same bytes"] = tiffWith(many, 0, make([]byte, 16<<10))
	// IFD0 says the next directory is IFD1, IFD1 says it is IFD0.
	ifd1 := dataAt(1)
	loop := tiffWith([]rawEntry{{0x0112, 3, 1, 6}}, ifd1, rawDir([]rawEntry{{0x0112, 3, 1, 3}}, 8))
	cases["a loop of directories"] = loop
	// The sub-IFD pointers name IFD0 itself and each other.
	cases["sub-IFDs that name themselves"] = tiffWith([]rawEntry{{tagExifIFD, 4, 1, 8}, {tagGPSIFD, 4, 1, 8}, {0xA005, 4, 1, 8}}, 0, nil)
	return cases
}

// allocated is what f allocates, in bytes.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// within runs f, failing the test when it doesn't come back in time.
func within(t *testing.T, name string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: no answer after 20 s", name)
	}
}

// What a crafted EXIF costs to read is bounded, however it lies about its
// size - as a JPEG's EXIF, a HEIC's Exif item and a TIFF read at offsets.
func TestCraftedEXIFIsCheapToRead(t *testing.T) {
	const most = 8 << 20
	for name, tiff := range crafted() {
		item := append([]byte("Exif\x00\x00"), tiff...)
		for how, read := range map[string]func() (*Info, error){
			"JPEG":      func() (*Info, error) { return FromJPEG(inJPEG(tiff)) },
			"Exif item": func() (*Info, error) { return fromExifData(bytes.NewReader(item), int64(len(item))) },
			"TIFF": func() (*Info, error) {
				return FromJPEGReader(io.NewSectionReader(bytes.NewReader(tiff), 0, int64(len(tiff))))
			},
		} {
			var n uint64
			within(t, name+" as "+how, func() { n = allocated(func() { read() }) })
			if n > most {
				t.Errorf("%s as %s: %d MiB allocated", name, how, n>>20)
			}
		}
	}
}

// A malformed tag is left out on its own: the rest of the file is read.
func TestCraftedTagLeavesTheRest(t *testing.T) {
	model := append([]byte("NIKON D90"), 0)
	tiff := tiffWith([]rawEntry{
		{0x010F, 12, 0x20000001, dataAt(3)}, // Make: a double count past 32 bits
		{0x0110, 2, uint32(len(model)), dataAt(3) + 8},
		{0x0112, 3, 1, 6},
	}, 0, append(make([]byte, 8), model...))
	info, err := FromJPEG(inJPEG(tiff))
	if err != nil {
		t.Fatal(err)
	}
	if info.CameraMake != "" || info.CameraModel != "NIKON D90" || info.Orientation != 6 {
		t.Errorf("got %+v", info)
	}
}

// legacy is what goexif read of content by itself, as FromJPEG did until
// compactTIFF stood in front of it - for files whose structure is sound.
func legacy(t *testing.T, content []byte) *Info {
	t.Helper()
	x, err := exif.Decode(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("goexif: %v", err)
	}
	return fromExif(x)
}

// Wherever goexif read a sound file, what it reads through compactTIFF is
// the same: the real fixtures, and every tag fromExif reads, in each place
// goexif looks for it, in both byte orders.
func TestCompactReadsWhatGoexifRead(t *testing.T) {
	jpg, err := os.ReadFile("testdata/gps_test.jpg")
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string][]byte{"gps_test.jpg": jpg}
	short := func(id, v uint16) exifTag { return exifTag{id, 3, 1, binary.LittleEndian.AppendUint16(nil, v)} }
	ifd0 := []exifTag{asciiTag(0x010F, "Canon"), asciiTag(0x0110, "Canon EOS 5D Mark IV"), short(0x0112, 8), asciiTag(0x0132, "2020:02:02 10:00:00")}
	ex := []exifTag{
		asciiTag(0x9003, "2020:02:01 09:30:15"), asciiTag(0x9004, "2020:02:01 09:30:16"),
		rationalTag(0x829A, [2]uint32{1, 250}), rationalTag(0x829D, [2]uint32{28, 10}),
		short(0x8827, 400), rationalTag(0x920A, [2]uint32{50, 1}), longTag(0xA002, 6720), longTag(0xA003, 4480),
		asciiTag(0x9011, "+01:00"), asciiTag(0x9010, "+01:00"),
	}
	gps := []exifTag{
		asciiTag(0x0001, "N"), rationalTag(0x0002, [2]uint32{40, 1}, [2]uint32{26, 1}, [2]uint32{4614, 100}),
		asciiTag(0x0003, "W"), rationalTag(0x0004, [2]uint32{79, 1}, [2]uint32{58, 1}, [2]uint32{5600, 100}),
		rationalTag(0x0007, [2]uint32{8, 1}, [2]uint32{30, 1}, [2]uint32{15, 1}), asciiTag(0x001D, "2020:02:01"),
	}
	inputs["every tag"] = exifJPEG(ifd0, ex, gps)
	inputs["IFD0 only"] = exifJPEG(ifd0, nil, nil)
	inputs["the Exif sub-IFD's tags in IFD0"] = exifJPEG(append(append([]exifTag{}, ifd0...), ex[:8]...), nil, gps)
	inputs["a GPS time and no position"] = exifJPEG(nil, ex[:1], gps[4:])
	for name, content := range inputs {
		want := legacy(t, content)
		got, err := FromJPEG(content)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}
	// The same TIFF big-endian, and as a TIFF file of its own.
	little := inputs["every tag"][12 : len(inputs["every tag"])-2]
	big := swapTIFF(t, little)
	if got, err := FromJPEG(inJPEG(big)); err != nil || !reflect.DeepEqual(got, legacy(t, inJPEG(big))) {
		t.Errorf("big-endian: %+v, %v", got, err)
	}
	if got, err := FromJPEG(big); err != nil || !reflect.DeepEqual(got, legacy(t, big)) {
		t.Errorf("a TIFF: %+v, %v", got, err)
	}
}

// swapTIFF is the little-endian TIFF exifJPEG writes in big-endian:
// every directory entry and value turned to the other byte order.
func swapTIFF(t *testing.T, le []byte) []byte {
	t.Helper()
	be := make([]byte, len(le))
	copy(be, le)
	copy(be, "MM\x00*")
	binary.BigEndian.PutUint32(be[4:], binary.LittleEndian.Uint32(le[4:]))
	var swapDir func(off uint32)
	swapDir = func(off uint32) {
		n := binary.LittleEndian.Uint16(le[off:])
		binary.BigEndian.PutUint16(be[off:], n)
		for i := uint32(0); i < uint32(n); i++ {
			e := off + 2 + 12*i
			id, typ, count := binary.LittleEndian.Uint16(le[e:]), binary.LittleEndian.Uint16(le[e+2:]), binary.LittleEndian.Uint32(le[e+4:])
			binary.BigEndian.PutUint16(be[e:], id)
			binary.BigEndian.PutUint16(be[e+2:], typ)
			binary.BigEndian.PutUint32(be[e+4:], count)
			size := uint32(typeSizes[typ])
			at := e + 8
			if size*count > 4 {
				at = binary.LittleEndian.Uint32(le[e+8:])
				binary.BigEndian.PutUint32(be[e+8:], at)
			}
			for v := uint32(0); v < count; v++ {
				p := at + v*size
				switch typ {
				case 3:
					binary.BigEndian.PutUint16(be[p:], binary.LittleEndian.Uint16(le[p:]))
				case 4:
					binary.BigEndian.PutUint32(be[p:], binary.LittleEndian.Uint32(le[p:]))
				case 5:
					binary.BigEndian.PutUint32(be[p:], binary.LittleEndian.Uint32(le[p:]))
					binary.BigEndian.PutUint32(be[p+4:], binary.LittleEndian.Uint32(le[p+4:]))
				}
			}
			if id == tagExifIFD || id == tagGPSIFD {
				swapDir(binary.LittleEndian.Uint32(le[e+8:]))
			}
		}
		next := off + 2 + 12*uint32(n)
		binary.BigEndian.PutUint32(be[next:], binary.LittleEndian.Uint32(le[next:]))
	}
	swapDir(binary.LittleEndian.Uint32(le[4:]))
	return be
}

// A TIFF - a scan, a camera raw - is read where its tags are: a few KiB
// of one whose directory is after 8 MiB of picture.
func TestTIFFIsReadWhereItsTagsAre(t *testing.T) {
	make_ := append([]byte("Hasselblad"), 0)
	picture := 8 << 20
	dirAt := uint32(8 + picture)
	b := append([]byte("II*\x00"), binary.LittleEndian.AppendUint32(nil, dirAt)...)
	b = append(b, make([]byte, picture)...)
	b = append(b, rawDir([]rawEntry{{0x010F, 2, uint32(len(make_)), dirAt + 30}, {0x0112, 3, 1, 3}}, 0)...)
	b = append(b, make_...)
	c := &countingReader{r: bytes.NewReader(b)}
	info, err := FromJPEGReader(io.NewSectionReader(c, 0, int64(len(b))))
	if err != nil || info.CameraMake != "Hasselblad" || info.Orientation != 3 {
		t.Fatalf("got %+v, %v", info, err)
	}
	if c.n > 64<<10 {
		t.Errorf("read %d bytes of %d", c.n, len(b))
	}
}

// XMP's APP1 segment before EXIF's is passed over (goexif took the first
// APP1 for the EXIF one and found none).
func TestEXIFAfterXMP(t *testing.T) {
	jpg := exifJPEG([]exifTag{asciiTag(0x0110, "iPhone 15")}, nil, nil)
	xmp := []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")
	seg := append([]byte{0xFF, 0xE1}, binary.BigEndian.AppendUint16(nil, uint16(len(xmp)+2))...)
	withXMP := append(append([]byte{0xFF, 0xD8}, append(seg, xmp...)...), jpg[2:]...)
	info, err := FromJPEG(withXMP)
	if err != nil || info.CameraModel != "iPhone 15" {
		t.Errorf("got %+v, %v", info, err)
	}
}
