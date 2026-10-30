// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesquality

import (
	"image"
	"math/bits"
)

// The measures below read a photo's big thumbnail (at most 1000 px on its
// longest side), never the original: every photo is measured at the same
// scale, so a 50 MP photo isn't sharper than a 12 MP one just for its size.

// Light is how a photo is exposed.
type Light struct {
	// Dark and Bright are the shares of pixels clipped to black (luma at
	// most cDarkLuma) and to white (at least cBrightLuma), 0 to 1.
	Dark, Bright float64
	// Mean is the mean luma, 0 to 255.
	Mean float64
}

const (
	cDarkLuma   = 8
	cBrightLuma = 247
)

// gray is img's luma, one byte a pixel, row by row (w x h): a JPEG decodes
// to YCbCr, whose Y plane is it already.
func gray(img image.Image) (pix []uint8, w, h int) {
	b := img.Bounds()
	w, h = b.Dx(), b.Dy()
	pix = make([]uint8, w*h)
	if s, ok := img.(*image.YCbCr); ok {
		for y := 0; y < h; y++ {
			copy(pix[y*w:(y+1)*w], s.Y[s.YOffset(b.Min.X, b.Min.Y+y):])
		}
		return pix, w, h
	}
	if s, ok := img.(*image.Gray); ok {
		for y := 0; y < h; y++ {
			copy(pix[y*w:(y+1)*w], s.Pix[s.PixOffset(b.Min.X, b.Min.Y+y):])
		}
		return pix, w, h
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl := rgbAt(img, b.Min.X+x, b.Min.Y+y)
			// ITU-R BT.601, as JPEG's Y.
			pix[y*w+x] = uint8((19595*uint32(r) + 38470*uint32(g) + 7471*uint32(bl) + 1<<15) >> 16)
		}
	}
	return pix, w, h
}

// Sharpness is the variance of img's Laplacian (the 4-neighbour kernel) on
// its luma: sharp edges give a wide spread, a blurred photo almost none. The
// face filter uses the same measure on a face's crop
// (face_recognition.cMinFaceSharpness). 0 for an image too small to have
// an inside.
func Sharpness(img image.Image) float64 {
	pix, w, h := gray(img)
	if w < 3 || h < 3 {
		return 0
	}
	var sum, sumSq float64
	n := 0
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			i := row + x
			v := int(pix[i-w]) + int(pix[i+w]) + int(pix[i-1]) + int(pix[i+1]) - 4*int(pix[i])
			sum += float64(v)
			sumSq += float64(v * v)
			n++
		}
	}
	mean := sum / float64(n)
	return sumSq/float64(n) - mean*mean
}

// Exposure is img's Light.
func Exposure(img image.Image) Light {
	pix, _, _ := gray(img)
	if len(pix) == 0 {
		return Light{}
	}
	var dark, bright int
	var total uint64
	for _, v := range pix {
		total += uint64(v)
		if v <= cDarkLuma {
			dark++
		} else if v >= cBrightLuma {
			bright++
		}
	}
	n := float64(len(pix))
	return Light{Dark: float64(dark) / n, Bright: float64(bright) / n, Mean: float64(total) / n}
}

// DHash is img's 64-bit difference hash: its luma shrunk to 9x8 by
// averaging, each bit whether a cell is brighter than the one on its
// right. Two shots of one burst differ in a few bits (Distance), two
// different scenes in about half.
func DHash(img image.Image) uint64 {
	pix, w, h := gray(img)
	if w == 0 || h == 0 {
		return 0
	}
	var cells [8][9]float64
	for cy := 0; cy < 8; cy++ {
		y0, y1 := cy*h/8, max((cy+1)*h/8, cy*h/8+1)
		for cx := 0; cx < 9; cx++ {
			x0, x1 := cx*w/9, max((cx+1)*w/9, cx*w/9+1)
			var sum, n float64
			for y := y0; y < y1 && y < h; y++ {
				for x := x0; x < x1 && x < w; x++ {
					sum += float64(pix[y*w+x])
					n++
				}
			}
			if n > 0 {
				cells[cy][cx] = sum / n
			}
		}
	}
	var hash uint64
	for cy := 0; cy < 8; cy++ {
		for cx := 0; cx < 8; cx++ {
			hash <<= 1
			if cells[cy][cx] > cells[cy][cx+1] {
				hash |= 1
			}
		}
	}
	return hash
}

// Distance is how many bits two difference hashes differ in, 0 to 64.
func Distance(a, b uint64) int { return bits.OnesCount64(a ^ b) }
