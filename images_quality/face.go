// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesquality

import (
	"image"
	"math"
)

// A face's looks, for the face People shows for a person when the owner
// hasn't chosen one (owner, 2026-10-31: "it would be better to get the best
// looking one"). Measured on the recognizer's aligned crop (its eyes level
// and in the same place every time) and the face's size in its photo:
//   - sharp: the crop's Laplacian variance (Sharpness), on a log scale - a
//     soft or upscaled face has little detail;
//   - size: the face's shorter side in the photo, 40 to 400 pixels on a log
//     scale - a small face is a blurry one once enlarged;
//   - light: neither clipped to black or white nor dim or washed out;
//   - frontal: how alike the crop's two halves are, mirrored - a face
//     looking at the camera is close to symmetric, a profile or a face
//     half in shadow isn't.
//
// FaceQuality is their weighted sum, 0 to 1; the photo's own score (NIMA)
// is added by the query that ranks a person's faces (dao.BestFaces).
const (
	cFaceSharpWeight   = 0.35
	cFaceSizeWeight    = 0.30
	cFaceLightWeight   = 0.15
	cFaceFrontalWeight = 0.20

	// Laplacian variance of a 112-pixel crop: ~4 heavily blurred, a few
	// thousand in focus (face_recognition.cMinFaceSharpness).
	cFaceSharpLow  = 30.0
	cFaceSharpHigh = 3000.0
	cFaceSideLow   = 40.0
	cFaceSideHigh  = 400.0
)

// FaceQuality scores a face 0 to 1: crop is the aligned face, side the
// shorter side of its box in the photo, in pixels.
func FaceQuality(crop image.Image, side int) float64 {
	return cFaceSharpWeight*faceSharp(Sharpness(crop)) +
		cFaceSizeWeight*faceSize(side) +
		cFaceLightWeight*faceLight(Exposure(crop)) +
		cFaceFrontalWeight*faceFrontal(crop)
}

// logScale is v between low and high on a log scale, clamped to 0..1.
func logScale(v, low, high float64) float64 {
	if v <= low {
		return 0
	}
	if v >= high {
		return 1
	}
	return math.Log(v/low) / math.Log(high/low)
}

func faceSharp(variance float64) float64 { return logScale(variance, cFaceSharpLow, cFaceSharpHigh) }

func faceSize(side int) float64 { return logScale(float64(side), cFaceSideLow, cFaceSideHigh) }

// faceLight is 1 for a face lit mid-way, less as its mean luma leaves 70
// to 190, and less again for each share of it clipped.
func faceLight(l Light) float64 {
	v := 1.0
	switch {
	case l.Mean < 70:
		v = l.Mean / 70
	case l.Mean > 190:
		v = (255 - l.Mean) / 65
	}
	v -= 2 * (l.Dark + l.Bright)
	return math.Max(0, math.Min(1, v))
}

// faceFrontal is 1 minus the mean difference between each pixel of the
// crop's luma and its mirror across the middle, scaled so a difference of
// 64 levels (a profile, a face half in shadow) is 0.
func faceFrontal(crop image.Image) float64 {
	pix, w, h := gray(crop)
	if w < 2 || h < 1 {
		return 0
	}
	var total int
	for y := 0; y < h; y++ {
		row := pix[y*w : (y+1)*w]
		for x := 0; x < w/2; x++ {
			d := int(row[x]) - int(row[w-1-x])
			if d < 0 {
				d = -d
			}
			total += d
		}
	}
	mean := float64(total) / float64(h*(w/2))
	return math.Max(0, 1-mean/64)
}
