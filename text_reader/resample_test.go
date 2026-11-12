// SPDX-License-Identifier: AGPL-3.0-or-later

package textreader

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"math/rand"
	"os"
	"testing"
)

// pillowPattern is the image testdata/pillow_resize.json was made from
// (pillow_ref.py: Pillow's resize of it, by sha256 of the R,G,B bytes).
func pillowPattern(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var px [3]uint8
			for c := range px {
				px[c] = uint8((x*x*7 + y*y*3 + x*y*5 + c*85 + (x^y)*11) % 256)
			}
			img.SetNRGBA(x, y, color.NRGBA{px[0], px[1], px[2], 0xff})
		}
	}
	return img
}

// The resize is Pillow's to the byte: down, up, one side only, a crop, the
// same size.
func TestResizeMatchesPillow(t *testing.T) {
	b, err := os.ReadFile("testdata/pillow_resize.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		W, H  int
		Cases []struct {
			Filter string
			Crop   [4]int
			Size   [2]int
			Sha256 string
			Head   string
		}
	}
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	img := pillowPattern(ref.W, ref.H)
	for _, c := range ref.Cases {
		f := map[string]filter{"bilinear": bilinear, "bicubic": bicubic}[c.Filter]
		got := resizeRGB(img, image.Rect(c.Crop[0], c.Crop[1], c.Crop[2], c.Crop[3]), c.Size[0], c.Size[1], f)
		if len(got) != c.Size[0]*c.Size[1]*3 {
			t.Fatalf("%v: %d bytes", c, len(got))
		}
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != c.Sha256 {
			head, _ := base64.StdEncoding.DecodeString(c.Head)
			t.Errorf("%s %v -> %v: not Pillow's bytes; head %v, Pillow's %v", c.Filter, c.Crop, c.Size, got[:len(head)], head)
		}
	}
}

// A sub-image reads as the same pixels as a copy of it, whatever its type.
func TestResizeSubImage(t *testing.T) {
	img := pillowPattern(61, 37)
	sub := img.SubImage(image.Rect(5, 3, 50, 30))
	want := resizeRGB(img, image.Rect(5, 3, 50, 30), 16, 16, bicubic)
	if got := resizeRGB(sub, sub.Bounds(), 16, 16, bicubic); !bytes.Equal(got, want) {
		t.Fatal("a sub-image resizes differently from its rectangle")
	}
}

// readRow's fast paths give what the slow path (At, straight colours)
// gives.
func TestReadRowFastPaths(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	fill := func(b []byte) {
		for i := range b {
			b[i] = byte(rng.Intn(256))
		}
	}
	r := image.Rect(0, 0, 13, 9)
	imgs := map[string]image.Image{}
	for _, ratio := range []image.YCbCrSubsampleRatio{image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422,
		image.YCbCrSubsampleRatio420, image.YCbCrSubsampleRatio440, image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410} {
		y := image.NewYCbCr(r, ratio)
		fill(y.Y)
		fill(y.Cb)
		fill(y.Cr)
		imgs["ycbcr "+ratio.String()] = y
		imgs["ycbcr sub-image "+ratio.String()] = y.SubImage(image.Rect(3, 2, 12, 9))
	}
	rgba := image.NewRGBA(r)
	fill(rgba.Pix)
	for i := 3; i < len(rgba.Pix); i += 4 { // valid premultiplied colours, some see-through
		if i%3 == 0 {
			rgba.Pix[i] = 0xff
		}
		for c := 1; c <= 3; c++ {
			rgba.Pix[i-c] = min(rgba.Pix[i-c], rgba.Pix[i])
		}
	}
	imgs["rgba"] = rgba
	imgs["rgba sub-image"] = rgba.SubImage(image.Rect(1, 1, 7, 8))
	nrgba := image.NewNRGBA(r)
	fill(nrgba.Pix)
	imgs["nrgba"] = nrgba
	gray := image.NewGray(r)
	fill(gray.Pix)
	imgs["gray"] = gray
	imgs["gray sub-image"] = gray.SubImage(image.Rect(2, 1, 9, 5))

	for name, img := range imgs {
		b := img.Bounds()
		got := make([]byte, b.Dx()*3)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			readRow(img, y, b.Min.X, b.Max.X, got)
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
				i := 3 * (x - b.Min.X)
				if got[i] != c.R || got[i+1] != c.G || got[i+2] != c.B {
					t.Fatalf("%s (%d, %d): %v, want %v", name, x, y, got[i:i+3], c)
				}
			}
		}
	}
}
