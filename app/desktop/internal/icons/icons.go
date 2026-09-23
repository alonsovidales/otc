// SPDX-License-Identifier: AGPL-3.0-or-later

// Package icons draws the tray icon: RaidMenuIcon.swift's rack with two
// drive bars, each coloured by the RAID's state (issue #69). Drawn at
// runtime so no image assets are needed; PNG for Linux, ICO for Windows.
package icons

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"runtime"
)

// Colours per health, top and bottom drive.
var palette = map[string][2]color.RGBA{
	"ok":       {{0x34, 0xC7, 0x59, 0xFF}, {0x34, 0xC7, 0x59, 0xFF}},
	"degraded": {{0xFF, 0x3B, 0x30, 0xFF}, {0xFF, 0x95, 0x00, 0xFF}},
	"failed":   {{0xFF, 0x3B, 0x30, 0xFF}, {0xFF, 0x3B, 0x30, 0xFF}},
	"unknown":  {{0x9A, 0x9A, 0x9A, 0xFF}, {0x9A, 0x9A, 0x9A, 0xFF}},
}

// For returns the icon bytes for a RAID health, in the format this OS's
// tray wants.
func For(health string) []byte {
	img := draw(health, 32)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	if runtime.GOOS == "windows" {
		return ico(buf.Bytes(), 32)
	}

	return buf.Bytes()
}

func draw(health string, size int) *image.RGBA {
	c, ok := palette[health]
	if !ok {
		c = palette["unknown"]
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	outline := color.RGBA{0xE6, 0xE6, 0xE6, 0xFF}
	inset := 2
	// The rack: a rounded rectangle outline, two pixels thick.
	for x := inset; x < size-inset; x++ {
		for y := inset; y < size-inset; y++ {
			edge := x < inset+2 || x >= size-inset-2 || y < inset+2 || y >= size-inset-2
			corner := (x < inset+2 && y < inset+2) || (x < inset+2 && y >= size-inset-2) ||
				(x >= size-inset-2 && y < inset+2) || (x >= size-inset-2 && y >= size-inset-2)
			if edge && !corner {
				img.SetRGBA(x, y, outline)
			}
		}
	}
	// Two bars with three gaps.
	gap := 4
	barH := (size - 2*inset - 4 - 3*gap) / 2
	barX0, barX1 := inset+2+gap, size-inset-2-gap
	top0 := inset + 2 + gap
	fill := func(y0 int, col color.RGBA) {
		for y := y0; y < y0+barH; y++ {
			for x := barX0; x < barX1; x++ {
				img.SetRGBA(x, y, col)
			}
		}
	}
	fill(top0, c[0])
	fill(top0+barH+gap, c[1])

	return img
}

// ico wraps a PNG in the ICO container (one image); Windows accepts PNG
// payloads in ICO since Vista.
func ico(pngData []byte, size int) []byte {
	var b bytes.Buffer
	// ICONDIR: reserved, type 1, count 1
	_ = binary.Write(&b, binary.LittleEndian, uint16(0))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	// ICONDIRENTRY
	b.WriteByte(byte(size))                               // width
	b.WriteByte(byte(size))                               // height
	b.WriteByte(0)                                        // colours
	b.WriteByte(0)                                        // reserved
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))  // planes
	_ = binary.Write(&b, binary.LittleEndian, uint16(32)) // bpp
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pngData)))
	_ = binary.Write(&b, binary.LittleEndian, uint32(6+16)) // offset
	b.Write(pngData)

	return b.Bytes()
}
