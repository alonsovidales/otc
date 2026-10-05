// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// testdata/tile64.hevc is one 64x64 HEVC frame (ffmpeg's testsrc, libx265,
// Annex B: VPS, SPS, PPS and the picture), the tile heicGrid builds a grid
// HEIC from.

func bmffBox(typ string, payload ...[]byte) []byte {
	var body []byte
	for _, p := range payload {
		body = append(body, p...)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(out, typ...), body...)
}

func bmffFullBox(typ string, version byte, payload ...[]byte) []byte {
	return bmffBox(typ, append([][]byte{{version, 0, 0, 0}}, payload...)...)
}

func u16(v int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(v)) }
func u32(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }

// heicGrid is a HEIC whose primary item is a cols x rows grid naming the
// same 64x64 tile for every cell, and whose header says it is w x h.
func heicGrid(t *testing.T, cols, rows, w, h int) []byte {
	t.Helper()
	stream, err := os.ReadFile("testdata/tile64.hevc")
	if err != nil {
		t.Fatal(err)
	}
	// Annex B NAL units: parameter sets to hvcC, the picture to the item.
	var params [][]byte
	var picture []byte
	for _, nal := range bytes.Split(stream, []byte{0, 0, 1}) {
		nal = bytes.TrimSuffix(nal, []byte{0})
		if len(nal) == 0 {
			continue
		}
		if typ := (nal[0] >> 1) & 0x3f; typ >= 32 && typ <= 34 {
			params = append(params, nal)
		} else {
			picture = append(append(picture, u32(len(nal))...), nal...)
		}
	}
	hvcc := []byte{1, 1, 0x60, 0, 0, 0, 0x90, 0, 0, 0, 0, 0, 30, 0xf0, 0, 0xfc, 0xfd, 0xf8, 0xf8, 0, 0, 0x0f, byte(len(params))}
	for _, p := range params {
		hvcc = append(hvcc, 0x80|(p[0]>>1)&0x3f)
		hvcc = append(append(append(hvcc, u16(1)...), u16(len(p))...), p...)
	}
	grid := []byte{0, 0, byte(rows - 1), byte(cols - 1), 0, 0, 0, 0}
	refs := append(u16(1), u16(cols*rows)...)
	for i := 0; i < cols*rows; i++ {
		refs = append(refs, u16(2)...)
	}
	ftyp := bmffBox("ftyp", []byte("heic"), u32(0), []byte("mif1heic"))
	meta := func(gridAt, tileAt int) []byte {
		return bmffFullBox("meta", 0,
			bmffFullBox("hdlr", 0, u32(0), []byte("pict"), make([]byte, 13)),
			bmffFullBox("pitm", 0, u16(1)),
			bmffFullBox("iinf", 0, u16(2),
				bmffFullBox("infe", 2, u16(1), u16(0), []byte("grid\x00")),
				bmffFullBox("infe", 2, u16(2), u16(0), []byte("hvc1\x00"))),
			bmffFullBox("iref", 0, bmffBox("dimg", refs)),
			bmffBox("iprp",
				bmffBox("ipco", bmffBox("hvcC", hvcc), bmffFullBox("ispe", 0, u32(w), u32(h)), bmffFullBox("ispe", 0, u32(64), u32(64))),
				bmffFullBox("ipma", 0, u32(2), u16(1), []byte{1, 0x80 | 2}, u16(2), []byte{2, 0x80 | 1, 3})),
			bmffFullBox("iloc", 0, []byte{0x44, 0}, u16(2),
				u16(1), u16(0), u16(1), u32(gridAt), u32(len(grid)),
				u16(2), u16(0), u16(1), u32(tileAt), u32(len(picture))))
	}
	at := len(ftyp) + len(meta(0, 0)) + 8
	return append(append(ftyp, meta(at, at+len(grid))...), bmffBox("mdat", grid, picture)...)
}

// A grid that decodes to a sane size is let through and decodes as before.
func TestHeifGridOfASaneSizeDecodes(t *testing.T) {
	heic := heicGrid(t, 2, 2, 128, 128)
	if err := checkImageSize(heic); err != nil {
		t.Fatalf("a 128x128 grid was refused: %v", err)
	}
	img, err := decodeImage(heic)
	if err != nil {
		t.Fatalf("decoding the grid: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 128 || b.Dy() != 128 {
		t.Errorf("decoded to %v, want 128x128", b)
	}
}

// A small file saying it's 100x100 whose grid repeats one tile 40,000
// times - a 12800x12800 canvas - is refused before goheif allocates it.
func TestHeifGridBombIsRefused(t *testing.T) {
	heic := heicGrid(t, 200, 200, 100, 100)
	if len(heic) > 200<<10 {
		t.Fatalf("the bomb is %d bytes: not small", len(heic))
	}
	err := checkImageSize(heic)
	if !errors.Is(err, errImageTooLarge) {
		t.Fatalf("the grid bomb was let through: %v", err)
	}
	if _, err := decodeImage(heic); !errors.Is(err, errImageTooLarge) {
		t.Errorf("decodeImage: %v", err)
	}
}

// A header larger than the grid behind it would have its crop run past
// the canvas: refused too.
func TestHeifGridSmallerThanItsHeaderIsRefused(t *testing.T) {
	if err := checkImageSize(heicGrid(t, 2, 2, 200, 200)); err == nil {
		t.Error("a 128x128 grid saying it is 200x200 was let through")
	}
}
