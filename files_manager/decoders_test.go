// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// GIF, BMP and TIFF used to fail as "image: unknown format", so they never
// got a thumbnail (WebP has no Go encoder to build a fixture with; its
// decoder is registered the same way).
func TestMoreImageFormatsDecode(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	src.Set(1, 1, color.RGBA{R: 255, A: 255})
	encoders := map[string]func(*bytes.Buffer) error{
		"gif":  func(b *bytes.Buffer) error { return gif.Encode(b, src, nil) },
		"bmp":  func(b *bytes.Buffer) error { return bmp.Encode(b, src) },
		"tiff": func(b *bytes.Buffer) error { return tiff.Encode(b, src, nil) },
	}
	for name, enc := range encoders {
		var buf bytes.Buffer
		if err := enc(&buf); err != nil {
			t.Fatalf("%s: encoding the fixture: %v", name, err)
		}
		if _, format, err := image.Decode(&buf); err != nil || format != name {
			t.Errorf("%s: decode gave format %q, err %v", name, format, err)
		}
	}
}

// A ".HEIC" that is really a JPEG is recognised by its content.
func TestIsJPEGContent(t *testing.T) {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	if !isJPEGContent(buf.Bytes()) {
		t.Error("a JPEG was not recognised")
	}
	if isJPEGContent([]byte("\x00\x00\x00\x18ftypheic")) {
		t.Error("a HEIC was taken for a JPEG")
	}
}
