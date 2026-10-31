// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesquality

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// faceCrop draws a 112-pixel stand-in for an aligned face: a fine checker
// (detail) of contrast `detail` around `mean`, and `shade` subtracted from
// its right half (a profile or a face half in shadow).
func faceCrop(mean, detail, shade int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, 112, 112))
	for y := 0; y < 112; y++ {
		for x := 0; x < 112; x++ {
			v := mean
			// Mirrored across the middle, as a face looking ahead is.
			xs := x
			if x >= 56 {
				xs = 111 - x
			}
			if (xs+y)%2 == 0 {
				v += detail
			} else {
				v -= detail
			}
			if x >= 56 {
				v -= shade
			}
			img.SetGray(x, y, color.Gray{Y: uint8(max(0, min(255, v)))})
		}
	}
	return img
}

// Each measure on its own moves the score the right way.
func TestFaceQualityPrefersTheBetterLookingFace(t *testing.T) {
	good := FaceQuality(faceCrop(128, 40, 0), 300)
	for _, c := range []struct {
		name string
		q    float64
	}{
		{"blurred", FaceQuality(faceCrop(128, 1, 0), 300)},
		{"small", FaceQuality(faceCrop(128, 40, 0), 50)},
		{"dark", FaceQuality(faceCrop(30, 20, 0), 300)},
		{"washed out", FaceQuality(faceCrop(225, 20, 0), 300)},
		{"half in shadow", FaceQuality(faceCrop(128, 40, 70), 300)},
	} {
		if c.q >= good {
			t.Errorf("%s face scored %.3f, not below the good one's %.3f", c.name, c.q, good)
		}
	}
	if good < 0.8 || good > 1 {
		t.Errorf("a sharp, large, well-lit, frontal face scored %.3f", good)
	}
}

// The parts stay within 0..1 at their ends.
func TestFaceQualityParts(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"sharp floor", faceSharp(0), 0},
		{"sharp ceiling", faceSharp(1e6), 1},
		{"size floor", faceSize(10), 0},
		{"size ceiling", faceSize(4000), 1},
		{"size middle", faceSize(126), math.Log(126.0/40) / math.Log(10)},
		{"light mid", faceLight(Light{Mean: 128}), 1},
		{"light black", faceLight(Light{Mean: 0, Dark: 1}), 0},
		{"frontal symmetric", faceFrontal(faceCrop(128, 0, 0)), 1},
		{"frontal half dark", faceFrontal(faceCrop(128, 0, 128)), 0},
	} {
		if math.Abs(c.got-c.want) > 1e-9 {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}
