// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"fmt"
	"image"
	"math/rand"
	"testing"
)

func randomNRGBA(r *rand.Rand, rect image.Rectangle) *image.NRGBA {
	img := image.NewNRGBA(rect)
	r.Read(img.Pix)
	return img
}

func samePixels(t *testing.T, what string, got, want image.Image) {
	t.Helper()
	g, ok1 := got.(*image.NRGBA)
	w, ok2 := want.(*image.NRGBA)
	if !ok1 || !ok2 {
		t.Fatalf("%s: not NRGBA (%T, %T)", what, got, want)
	}
	if g.Rect != w.Rect || !bytes.Equal(g.Pix, w.Pix) {
		t.Errorf("%s: pixels differ (%v vs %v)", what, g.Rect, w.Rect)
	}
}

// The single pass heifOrientation picks gives exactly what the mirror
// pass and the rotation passes it replaced gave, for every irot/imir.
func TestHeifOrientationIsTheComposedPasses(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	src := randomNRGBA(rnd, image.Rect(0, 0, 3, 2)) // not square
	for _, mirror := range []struct {
		has  bool
		axis int
	}{{false, 0}, {true, 0}, {true, 1}} {
		for rotations := 0; rotations < 4; rotations++ {
			var composed image.Image = src
			if mirror.has {
				if mirror.axis == 1 {
					composed = applyOrientation(composed, 4)
				} else {
					composed = applyOrientation(composed, 2)
				}
			}
			for i := 0; i < rotations; i++ {
				composed = applyOrientation(composed, 8)
			}
			one := applyOrientation(src, heifOrientation(rotations, mirror.has, mirror.axis))
			samePixels(t, fmt.Sprintf("mirror %v axis %d, %d turns", mirror.has, mirror.axis, rotations), one, composed)
		}
	}
}

// opaque hides an image's type, so applyOrientation takes its generic path.
type opaque struct{ image.Image }

// Every fast path writes exactly the bytes the generic At/Set path does,
// for every orientation, chroma subsampling, odd size and sub-image.
func TestApplyOrientationFastPathsMatchTheGenericPath(t *testing.T) {
	rnd := rand.New(rand.NewSource(2))
	ycbcr := func(rect image.Rectangle, ratio image.YCbCrSubsampleRatio) *image.YCbCr {
		img := image.NewYCbCr(rect, ratio)
		rnd.Read(img.Y)
		rnd.Read(img.Cb)
		rnd.Read(img.Cr)
		return img
	}
	gray := image.NewGray(image.Rect(0, 0, 9, 4))
	rnd.Read(gray.Pix)
	sources := map[string]image.Image{
		"ycbcr 4:4:4":       ycbcr(image.Rect(0, 0, 7, 5), image.YCbCrSubsampleRatio444),
		"ycbcr 4:2:2":       ycbcr(image.Rect(0, 0, 7, 5), image.YCbCrSubsampleRatio422),
		"ycbcr 4:2:0":       ycbcr(image.Rect(0, 0, 7, 5), image.YCbCrSubsampleRatio420),
		"ycbcr sub-image":   ycbcr(image.Rect(0, 0, 11, 9), image.YCbCrSubsampleRatio420).SubImage(image.Rect(3, 1, 10, 6)),
		"ycbcr not at 0,0":  ycbcr(image.Rect(5, 3, 12, 8), image.YCbCrSubsampleRatio420),
		"nrgba":             randomNRGBA(rnd, image.Rect(0, 0, 6, 5)),
		"nrgba sub-image":   randomNRGBA(rnd, image.Rect(0, 0, 10, 8)).SubImage(image.Rect(2, 3, 9, 7)),
		"gray":              gray,
		"gray sub-image":    gray.SubImage(image.Rect(1, 1, 8, 4)),
		"rgba (generic)":    image.NewRGBA(image.Rect(0, 0, 3, 3)),
		"ycbcr 4:1:1 (odd)": ycbcr(image.Rect(0, 0, 9, 3), image.YCbCrSubsampleRatio411),
	}
	for name, src := range sources {
		for o := 2; o <= 8; o++ {
			samePixels(t, fmt.Sprintf("%s, orientation %d", name, o), applyOrientation(src, o), applyOrientation(opaque{src}, o))
		}
	}
}

// referenceOrientation is applyOrientation as it was before the fast
// paths: a per-pixel switch, At and Set.
func referenceOrientation(img image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dstW, dstH := w, h
	if orientation >= 5 {
		dstW, dstH = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.At(b.Min.X+x, b.Min.Y+y)
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, c)
		}
	}
	return dst
}

// The rewrite gives what the original gave, on every path.
func TestApplyOrientationMatchesTheOriginal(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	y := image.NewYCbCr(image.Rect(2, 1, 15, 8), image.YCbCrSubsampleRatio420)
	rnd.Read(y.Y)
	rnd.Read(y.Cb)
	rnd.Read(y.Cr)
	rgba := image.NewRGBA(image.Rect(0, 0, 5, 3))
	rnd.Read(rgba.Pix)
	for name, src := range map[string]image.Image{
		"ycbcr": y, "nrgba": randomNRGBA(rnd, image.Rect(1, 2, 8, 5)), "rgba": rgba,
	} {
		for o := 2; o <= 8; o++ {
			samePixels(t, fmt.Sprintf("%s, orientation %d", name, o), applyOrientation(src, o), referenceOrientation(src, o))
		}
	}
}
