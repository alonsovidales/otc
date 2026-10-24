// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"image"
	"image/color"
	"math"
)

// decodeBytesPerPixel is roughly what Go's decoder of format holds per
// pixel of b at its peak, m being the color model its header gives
// (image.DecodeConfig) - what stillCost reserves and checks:
//
//   - a JPEG: a byte a sample (4:2:0 is 1.5 samples a pixel, 4:4:4 three,
//     jpegFrame), and a progressive one also keeps every coefficient as an
//     int32 until its last scan (4 bytes a sample); an RGB or CMYK one is
//     converted at the end (4 bytes more);
//   - a PNG: its pixels (1 byte to 8), twice for an interlaced one, whose
//     seven passes are decoded apart and then merged;
//   - a TIFF: its pixels and a strip's raw samples, a whole image's for the
//     usual single strip;
//   - a WebP: 4:2:0 for a lossy one, a uint32 a pixel and then an NRGBA
//     for a lossless one;
//   - a HEIC: cHeicBytesPerPixel.
func decodeBytesPerPixel(b []byte, format string, m color.Model) float64 {
	switch format {
	case "jpeg":
		samples, progressive, ok := jpegFrame(b)
		if !ok {
			samples, progressive = 4, true // the worst it can be
		}
		n := samples
		if progressive {
			n += 4 * samples
		}
		if m == color.RGBAModel || m == color.CMYKModel {
			n += 4
		}
		return n
	case "heic":
		return cHeicBytesPerPixel
	case "png":
		n := modelBytes(m)
		// IHDR's interlace method, at a fixed place after the signature.
		if len(b) > 28 && b[28] == 1 {
			n *= 2
		}
		return n
	case "tiff":
		return 2 * modelBytes(m)
	case "webp":
		switch m {
		case color.YCbCrModel:
			return 1.5
		case color.NYCbCrAModel:
			return 2.5
		}
		return 8
	}
	return modelBytes(m)
}

// modelBytes is how many bytes a pixel of an image in model m takes.
func modelBytes(m color.Model) float64 {
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	switch m {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.YCbCrModel:
		return 3 // 4:4:4 at most
	case color.RGBA64Model, color.NRGBA64Model:
		return 8
	}
	return 4 // RGBA, NRGBA, CMYK, NYCbCrA and anything else
}

// jpegFrame reads a JPEG's frame header (SOFn): how many samples a pixel
// has across its components - 1.5 for 4:2:0, 3 for 4:4:4 - and whether
// it is progressive. ok is false when there is no frame header before the
// first scan.
func jpegFrame(b []byte) (samples float64, progressive, ok bool) {
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xff {
			return 0, false, false
		}
		m := b[i+1]
		switch {
		case m == 0xff: // fill
			i++
			continue
		case m == 0x01 || m == 0xd8 || (m >= 0xd0 && m <= 0xd7): // no length
			i += 2
			continue
		case m == 0xda || m == 0xd9: // a scan, or the end: no frame
			return 0, false, false
		}
		n := int(b[i+2])<<8 | int(b[i+3])
		if n < 2 { // a length counts its own two bytes
			return 0, false, false
		}
		// SOF0-15, but DHT, JPG and DAC, which share the range.
		if m >= 0xc0 && m <= 0xcf && m != 0xc4 && m != 0xc8 && m != 0xcc {
			seg := b[i+4 : min(len(b), i+2+n)]
			if len(seg) < 6 {
				return 0, false, false
			}
			nf := int(seg[5])
			if nf == 0 || len(seg) < 6+3*nf {
				return 0, false, false
			}
			hmax, vmax := 1, 1
			for c := 0; c < nf; c++ {
				hv := seg[6+3*c+1]
				hmax, vmax = max(hmax, int(hv>>4)), max(vmax, int(hv&0x0f))
			}
			for c := 0; c < nf; c++ {
				hv := seg[6+3*c+1]
				samples += float64(int(hv>>4)*int(hv&0x0f)) / float64(hmax*vmax)
			}
			return samples, m == 0xc2 || m == 0xc6 || m == 0xca || m == 0xce, samples > 0
		}
		i += 2 + n
	}
	return 0, false, false
}

// shrinkForWork is img brought down to at most cWorkPixels by the
// smallest whole factor k that gets it there, each pixel the average of
// the k x k it stands for (by their alpha), as an NRGBA - or img itself
// when it isn't over. A box rather than the thumbnails' Catmull-Rom: one
// pass, no buffer, and everything made from it is scaled down further.
func shrinkForWork(img image.Image) image.Image {
	return shrinkTo(img, cWorkPixels)
}

// shrinkTo is shrinkForWork for at most maxPixels.
func shrinkTo(img image.Image, maxPixels int64) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if int64(w)*int64(h) <= maxPixels {
		return img
	}
	k := shrinkFactor(w, h, maxPixels)
	dw, dh := (w+k-1)/k, (h+k-1)/k
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	if p, ok := img.(*image.YCbCr); ok {
		shrinkYCbCr(dst, p, k)
		return dst
	}
	at := premultiplied(img)
	sum := make([][4]uint64, dw)
	for dy := 0; dy < dh; dy++ {
		clear(sum)
		y0, y1 := b.Min.Y+dy*k, min(b.Min.Y+(dy+1)*k, b.Max.Y)
		for y := y0; y < y1; y++ {
			for dx := range sum {
				s := &sum[dx]
				for x := b.Min.X + dx*k; x < min(b.Min.X+(dx+1)*k, b.Max.X); x++ {
					r, g, bl, a := at(x, y)
					s[0], s[1], s[2], s[3] = s[0]+uint64(r), s[1]+uint64(g), s[2]+uint64(bl), s[3]+uint64(a)
				}
			}
		}
		row := dst.Pix[dy*dst.Stride:]
		for dx, s := range sum {
			n := uint64(min(k, w-dx*k)) * uint64(y1-y0)
			p := row[dx*4 : dx*4+4 : dx*4+4]
			if s[3] == 0 {
				p[0], p[1], p[2], p[3] = 0, 0, 0, 0
				continue
			}
			// Back from premultiplied 16 bits to straight 8.
			for c := 0; c < 3; c++ {
				p[c] = uint8(min(255, (s[c]*255+s[3]/2)/s[3]))
			}
			p[3] = uint8((s[3] / n) >> 8)
		}
	}
	return dst
}

// shrinkFactor is the smallest whole k that brings w x h to at most
// maxPixels, a partial block at the edges counting as a pixel.
func shrinkFactor(w, h int, maxPixels int64) int {
	k := max(2, int(math.Ceil(math.Sqrt(float64(w)*float64(h)/float64(maxPixels)))))
	for k < max(w, h) && int64((w+k-1)/k)*int64((h+k-1)/k) > maxPixels {
		k++
	}
	return k
}

// shrinkYCbCr is shrinkTo for a JPEG's or HEIC's YCbCr - nearly every
// large photo: the samples are averaged as they are and converted once
// per pixel of dst.
func shrinkYCbCr(dst *image.NRGBA, p *image.YCbCr, k int) {
	b := p.Rect
	w := b.Dx()
	sum := make([][3]uint32, dst.Rect.Dx())
	for dy := 0; dy < dst.Rect.Dy(); dy++ {
		clear(sum)
		y0, y1 := b.Min.Y+dy*k, min(b.Min.Y+(dy+1)*k, b.Max.Y)
		for y := y0; y < y1; y++ {
			for dx := range sum {
				s := &sum[dx]
				for x := b.Min.X + dx*k; x < min(b.Min.X+(dx+1)*k, b.Max.X); x++ {
					ci := p.COffset(x, y)
					s[0] += uint32(p.Y[p.YOffset(x, y)])
					s[1] += uint32(p.Cb[ci])
					s[2] += uint32(p.Cr[ci])
				}
			}
		}
		row := dst.Pix[dy*dst.Stride:]
		for dx, s := range sum {
			n := uint32(min(k, w-dx*k)) * uint32(y1-y0)
			r, g, bl := color.YCbCrToRGB(uint8((s[0]+n/2)/n), uint8((s[1]+n/2)/n), uint8((s[2]+n/2)/n))
			p := row[dx*4 : dx*4+4 : dx*4+4]
			p[0], p[1], p[2], p[3] = r, g, bl, 0xff
		}
	}
}

// premultiplied reads img's pixels as color.Color's RGBA does - 16 bits a
// channel, premultiplied - through each decoder's own type, so no pixel
// is boxed in an interface on the way (a 16-bit PNG, a CMYK JPEG, a WebP
// with alpha). A palette's colors already are.
func premultiplied(img image.Image) func(x, y int) (r, g, b, a uint32) {
	switch p := img.(type) {
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.RGBAAt(x, y).RGBA() }
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.NRGBAAt(x, y).RGBA() }
	case *image.RGBA64:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.RGBA64At(x, y).RGBA() }
	case *image.NRGBA64:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.NRGBA64At(x, y).RGBA() }
	case *image.Gray:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.GrayAt(x, y).RGBA() }
	case *image.Gray16:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.Gray16At(x, y).RGBA() }
	case *image.CMYK:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.CMYKAt(x, y).RGBA() }
	case *image.NYCbCrA:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.NYCbCrAAt(x, y).RGBA() }
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return p.YCbCrAt(x, y).RGBA() }
	}
	return func(x, y int) (uint32, uint32, uint32, uint32) { return img.At(x, y).RGBA() }
}
