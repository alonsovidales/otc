// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"strings"
	"testing"
)

// pngHeader is a PNG whose header claims w x h - enough for DecodeConfig,
// never decoded (that's the point: a few bytes claiming billions).
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

// Issue #165: a tiny file claiming a gigantic image is refused before it
// is decoded; a normal one goes through.
func TestDecompressionBombIsRefused(t *testing.T) {
	bomb := pngHeader(40000, 40000)
	if _, err := decodeImage(bomb); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("a 40000x40000 PNG was not refused: %v", err)
	}
	var ok bytes.Buffer
	png.Encode(&ok, image.NewRGBA(image.Rect(0, 0, 64, 48)))
	img, err := decodeImage(ok.Bytes())
	if err != nil || img.Bounds().Dx() != 64 {
		t.Fatalf("a normal PNG: %v", err)
	}
}

// A panic in background processing is contained, not fatal.
func TestSafelyRecovers(t *testing.T) {
	mg := &Manager{}
	ran := false
	mg.safely("testing", "/x", func() { ran = true; panic("boom") })
	if !ran {
		t.Fatal("the work didn't run")
	}
}
