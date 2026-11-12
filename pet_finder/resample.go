// SPDX-License-Identifier: AGPL-3.0-or-later

package petfinder

import (
	"image"
	"image/color"
	"math"
)

// Pillow's resize (Image.resize of an RGB image with BILINEAR or BICUBIC),
// ported to the letter - text_reader has the same file. The models were
// measured on Pillow's pixels, and the same arithmetic gives the same bytes
// from the same photo: filter weights in double precision, rounded to
// 22-bit fixed point, a horizontal pass to 8 bits, then a vertical one.
// golang.org/x/image/draw's kernels are close, not equal, and hold four
// float64s per pixel of their horizontal pass (~94 MB for a 12 MP photo's
// OCR input); this holds a few rows.

// filter is one of Pillow's resampling filters: its support and its weight
// at a distance.
type filter struct {
	support float64
	weight  func(x float64) float64
}

var (
	// bilinear is Pillow's BILINEAR: a triangle, wider when shrinking.
	bilinear = filter{1, func(x float64) float64 {
		if x < 0 {
			x = -x
		}
		if x < 1 {
			return 1 - x
		}
		return 0
	}}
	// bicubic is Pillow's BICUBIC: Keys' cubic with a = -0.5 (Catmull-Rom).
	bicubic = filter{2, func(x float64) float64 {
		const a = -0.5
		if x < 0 {
			x = -x
		}
		if x < 1 {
			return ((a+2)*x-(a+3))*x*x + 1
		}
		if x < 2 {
			return (((x-5)*x+8)*x - 4) * a
		}
		return 0
	}}
)

// precisionBits is Pillow's fixed point for 8-bit images.
const precisionBits = 32 - 8 - 2

// taps are one pass's weights: output i reads n[i] inputs from first[i],
// weighted by w[i*size:].
type taps struct {
	first, n []int
	w        []int32
	size     int
}

// coefficients is Pillow's precompute_coeffs and normalize_coeffs_8bpc,
// for in samples to out.
func coefficients(in, out int, f filter) taps {
	scale := float64(in) / float64(out)
	filterscale := scale
	if filterscale < 1 {
		filterscale = 1
	}
	support := f.support * filterscale
	size := int(math.Ceil(support))*2 + 1
	t := taps{first: make([]int, out), n: make([]int, out), w: make([]int32, out*size), size: size}
	k := make([]float64, size)
	ss := 1 / filterscale
	for xx := 0; xx < out; xx++ {
		center := (float64(xx) + 0.5) * scale
		xmin := int(center - support + 0.5)
		if xmin < 0 {
			xmin = 0
		}
		xmax := int(center + support + 0.5)
		if xmax > in {
			xmax = in
		}
		xmax -= xmin
		var ww float64
		for x := 0; x < xmax; x++ {
			w := f.weight((float64(x+xmin) - center + 0.5) * ss)
			k[x] = w
			ww += w
		}
		for x := 0; x < xmax; x++ {
			v := k[x]
			if ww != 0 {
				v /= ww
			}
			if v < 0 {
				t.w[xx*size+x] = int32(-0.5 + v*(1<<precisionBits))
			} else {
				t.w[xx*size+x] = int32(0.5 + v*(1<<precisionBits))
			}
		}
		t.first[xx], t.n[xx] = xmin, xmax
	}
	return t
}

// ycc is one channel of color.YCbCrToRGB from its 16.16 fixed point sum,
// clamped to a byte as it does, without branches.
func ycc(v int32) byte {
	v >>= 16
	v &^= v >> 31        // below 0: 0
	v |= (255 - v) >> 31 // above 255: all ones
	return byte(v)
}

func clip8(s int32) byte {
	if s >= 1<<(precisionBits+8) {
		return 255
	}
	if s <= 0 {
		return 0
	}
	return byte(s >> precisionBits)
}

// resizeRGB is Pillow's img.crop(r).resize((dw, dh), f) as R,G,B bytes,
// row after row; r is within img.Bounds(). The colours are those of
// Pillow's convert("RGB"): straight, alpha dropped. Like Pillow (13), it
// runs the vertical pass first when the height shrinks by more than twice
// what the width does, and skips a pass that changes nothing. The rows a
// pass reads are kept in a ring of as many as one output reads, so the
// memory is a few rows of the source, not a copy of it.
func resizeRGB(img image.Image, r image.Rectangle, dw, dh int, f filter) []byte {
	sw, sh := r.Dx(), r.Dy()
	needH, needV := dw != sw, dh != sh
	hFirst := !(sh-dh > 0 && sh-dh > (sw-dw)*2)
	out := make([]byte, dw*dh*3)
	// The source columns read: those the horizontal pass uses.
	x0, x1 := 0, sw
	var h taps
	if needH {
		h = coefficients(sw, dw, f)
		x0, x1 = h.first[0], h.first[dw-1]+h.n[dw-1]
	}
	src := func(y int, dst []byte) { readRow(img, r.Min.Y+y, r.Min.X+x0, r.Min.X+x1, dst) }
	horizontal := func(row, dst []byte) { // row holds columns x0..x1-1
		if !needH {
			copy(dst, row)
			return
		}
		for xx := 0; xx < dw; xx++ {
			k := h.w[xx*h.size : xx*h.size+h.n[xx]]
			p := row[(h.first[xx]-x0)*3:]
			s0, s1, s2 := int32(1<<(precisionBits-1)), int32(1<<(precisionBits-1)), int32(1<<(precisionBits-1))
			for i, w := range k {
				s0 += int32(p[3*i]) * w
				s1 += int32(p[3*i+1]) * w
				s2 += int32(p[3*i+2]) * w
			}
			dst[3*xx], dst[3*xx+1], dst[3*xx+2] = clip8(s0), clip8(s1), clip8(s2)
		}
	}
	if !needV {
		row := make([]byte, (x1-x0)*3)
		for y := 0; y < dh; y++ {
			src(y, row)
			horizontal(row, out[y*dw*3:(y+1)*dw*3])
		}
		return out
	}

	v := coefficients(sh, dh, f)
	// The ring: rows of the pass that runs first (the source as it is when
	// the vertical pass does), row y in slot y % v.size.
	width := x1 - x0 // the vertical pass first: the source's columns
	if hFirst {
		width = dw
	}
	ring := make([]byte, v.size*width*3)
	slot := func(y int) []byte { i := y % v.size; return ring[i*width*3 : (i+1)*width*3] }
	var row []byte
	if hFirst && needH {
		row = make([]byte, (x1-x0)*3)
	}
	mid := make([]byte, width*3) // the vertical pass's row, before the horizontal pass
	acc := make([]int32, width*3)
	next := 0 // the next row to put in the ring
	for yy := 0; yy < dh; yy++ {
		first, n := v.first[yy], v.n[yy]
		next = max(next, first) // rows above aren't read again
		for ; next < first+n; next++ {
			if hFirst && needH {
				src(next, row)
				horizontal(row, slot(next))
			} else {
				src(next, slot(next))
			}
		}
		for i := range acc {
			acc[i] = 1 << (precisionBits - 1)
		}
		for i, w := range v.w[yy*v.size : yy*v.size+n] {
			for x, b := range slot(first + i) {
				acc[x] += int32(b) * w
			}
		}
		o := out[yy*dw*3 : (yy+1)*dw*3]
		if hFirst {
			for x, s := range acc {
				o[x] = clip8(s)
			}
			continue
		}
		for x, s := range acc {
			mid[x] = clip8(s)
		}
		horizontal(mid, o)
	}
	return out
}

// readRow puts img's pixels x0..x1-1 of row y into dst as R,G,B bytes:
// straight colours, alpha dropped (Pillow's convert("RGB")). What photos
// decode to - a JPEG or HEIC's *image.YCbCr, an oriented or PNG photo's
// *image.NRGBA or *image.RGBA, a grey JPEG's *image.Gray - is read through
// its type; anything else pixel by pixel.
func readRow(img image.Image, y, x0, x1 int, dst []byte) {
	n := x1 - x0
	if n <= 0 {
		return
	}
	switch p := img.(type) {
	case *image.YCbCr:
		if x0 < 0 {
			break // COffset's arithmetic for negative x isn't a shift
		}
		var hs uint // chroma's horizontal subsampling, as a shift
		switch p.SubsampleRatio {
		case image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420:
			hs = 1
		case image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410:
			hs = 2
		}
		ys := p.Y[p.YOffset(x0, y):][:n]
		c0 := p.COffset(x0, y)
		cs := x0 >> hs // the chroma sample of x0
		cn := (x0+n-1)>>hs - cs + 1
		cbs, crs := p.Cb[c0:c0+cn], p.Cr[c0:c0+cn]
		d := dst[:3*n]
		for i, yv := range ys {
			j := (x0+i)>>hs - cs
			// color.YCbCrToRGB, inlined (it isn't, and a 12 MP photo is
			// 12M calls)
			yy1 := int32(yv) * 0x10101
			cb1, cr1 := int32(cbs[j])-128, int32(crs[j])-128
			d[3*i], d[3*i+1], d[3*i+2] = ycc(yy1+91881*cr1), ycc(yy1-22554*cb1-46802*cr1), ycc(yy1+116130*cb1)
		}
		return
	case *image.RGBA:
		s := p.Pix[p.PixOffset(x0, y):]
		for i := 0; i < n; i++ {
			px := s[4*i : 4*i+4]
			if px[3] == 0xff {
				dst[3*i], dst[3*i+1], dst[3*i+2] = px[0], px[1], px[2]
				continue
			}
			c := color.NRGBAModel.Convert(color.RGBA{px[0], px[1], px[2], px[3]}).(color.NRGBA)
			dst[3*i], dst[3*i+1], dst[3*i+2] = c.R, c.G, c.B
		}
		return
	case *image.NRGBA:
		s := p.Pix[p.PixOffset(x0, y):]
		for i := 0; i < n; i++ {
			dst[3*i], dst[3*i+1], dst[3*i+2] = s[4*i], s[4*i+1], s[4*i+2]
		}
		return
	case *image.Gray:
		s := p.Pix[p.PixOffset(x0, y):]
		for i := 0; i < n; i++ {
			dst[3*i], dst[3*i+1], dst[3*i+2] = s[i], s[i], s[i]
		}
		return
	}
	for i := 0; i < n; i++ {
		c := color.NRGBAModel.Convert(img.At(x0+i, y)).(color.NRGBA)
		dst[3*i], dst[3*i+1], dst[3*i+2] = c.R, c.G, c.B
	}
}
