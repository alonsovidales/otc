// SPDX-License-Identifier: AGPL-3.0-or-later

package petfinder

import (
	"bytes"
	"image"
	"image/jpeg"
)

// thumbSquare is the square a pet's thumbnail shows in a w x h photo,
// relative to its bounds: centred on the box x, y, bw, bh, as wide as the
// box's longer side - the photo's shorter side when that is less - and
// moved to stay inside the photo.
func thumbSquare(x, y, bw, bh, w, h int) image.Rectangle {
	side := min(max(bw, bh), w, h)
	// The box's centre, twice over to stay in whole pixels.
	x0 := clampInt(floorDiv(2*x+bw-side, 2), 0, w-side)
	y0 := clampInt(floorDiv(2*y+bh-side, 2), 0, h-side)
	return image.Rect(x0, y0, x0+side, y0+side)
}

// thumbnail is img's pixels in r (relative to its bounds) as a
// ThumbnailSize JPEG, nil for an empty r.
func thumbnail(img image.Image, r image.Rectangle) ([]byte, error) {
	if r.Empty() {
		return nil, nil
	}
	rgb := resizeRGB(img, r.Add(img.Bounds().Min), ThumbnailSize, ThumbnailSize, bicubic)
	t := image.NewRGBA(image.Rect(0, 0, ThumbnailSize, ThumbnailSize))
	for i := 0; i < ThumbnailSize*ThumbnailSize; i++ {
		t.Pix[4*i], t.Pix[4*i+1], t.Pix[4*i+2], t.Pix[4*i+3] = rgb[3*i], rgb[3*i+1], rgb[3*i+2], 0xff
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, t, &jpeg.Options{Quality: thumbnailQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func clampInt(v, lo, hi int) int {
	return min(max(v, lo), hi)
}
