// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"image"
	"os"
	"testing"

	"github.com/jdeng/goheif/heif"
)

// testdata/tenbit-grid.heic and eightbit-grid.heic are the same photo, a
// 3x2 grid of flat 64x64 tiles in cHeicTileColours, saved by libheif's
// heif-enc at 10 and 8 bits a sample (--cut-tiles 64, -b 10 for the first);
// tenbit-single.heic is it at 10 bits as one picture, no grid.
var cHeicTileColours = [6][3]int{{200, 40, 40}, {40, 180, 60}, {50, 70, 210}, {220, 200, 50}, {60, 190, 200}, {190, 60, 180}}

// decodeImageNoPanic is decodeImage with a panic as its third result, so a
// test can fail on it instead of crashing the run.
func decodeImageNoPanic(b []byte) (img image.Image, err error, panicked any) {
	defer func() { panicked = recover() }()
	img, err = decodeImage(b)
	return img, err, nil
}

// Phones that save HDR photos store HEIC at 10 bits a sample, two bytes
// each once decoded: a tile's rows are then twice as many bytes as it is
// wide. goheif placed each tile of a grid at x times those rows, past where
// it goes, and the last row ran past the end of the image: "slice bounds
// out of range [24640:24576]" for this one, [5243904:5242880] for a phone's
// 4x5 grid of 512x512 tiles - the panic the processing reported - and a
// picture with no grid came out as noise. Every tile must decode where it
// belongs, in its colour, at 8 bits as at 10.
func TestDecodeHeicGridAtTenBits(t *testing.T) {
	for _, name := range []string{"eightbit-grid.heic", "tenbit-grid.heic", "tenbit-single.heic"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			img, err, panicked := decodeImageNoPanic(b)
			if panicked != nil {
				t.Fatalf("decoding panicked: %v", panicked)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := img.Bounds(); got != image.Rect(0, 0, 192, 128) {
				t.Fatalf("bounds %v, want 192x128", got)
			}
			for i, want := range cHeicTileColours {
				x, y := (i%3)*64+32, (i/3)*64+32
				r, g, bl, _ := img.At(x, y).RGBA()
				got := [3]int{int(r >> 8), int(g >> 8), int(bl >> 8)}
				for c := range got {
					if d := got[c] - want[c]; d < -12 || d > 12 {
						t.Errorf("tile %d at %d,%d is %v, want about %v", i, x, y, got, want)
						break
					}
				}
			}
		})
	}
}

// The bit depth is read out of goheif's unexported hvcC record: a goheif
// that moves it fails here, not on a phone's photos (eightBit would then
// guess 10 bits).
func TestHeicBitDepth(t *testing.T) {
	for name, want := range map[string]int{"eightbit-grid.heic": 8, "tenbit-grid.heic": 10, "tenbit-single.heic": 10} {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		hf := heif.Open(bytes.NewReader(b))
		it, err := hf.PrimaryItem()
		if err != nil {
			t.Fatal(err)
		}
		if it.Info.ItemType == "grid" {
			if it, err = hf.ItemByID(it.Reference("dimg").ToItemIDs[0]); err != nil {
				t.Fatal(err)
			}
		}
		if got := heicBitDepth(it); got != want {
			t.Errorf("%s: bit depth %d, want %d", name, got, want)
		}
	}
}
