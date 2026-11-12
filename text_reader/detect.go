// SPDX-License-Identifier: AGPL-3.0-or-later

package textreader

import (
	"image"
	"image/color"
	"math"
	"sort"

	"gocv.io/x/gocv"
)

// dbBoxes is DB's post-processing of the detector's probability map
// (mw x mh): the lines' quads in a srcW x srcH photo's pixels, as
// PaddleOCR's DBPostProcess and the benchmark find them (but for unclip's
// rounding), in OpenCV's contour order.
func dbBoxes(prob []float32, mw, mh, srcW, srcH int) ([][8]float32, error) {
	bits := make([]byte, mw*mh)
	for i, p := range prob {
		if p > detThreshold {
			bits[i] = 1
		}
	}
	bitmap, err := gocv.NewMatFromBytes(mh, mw, gocv.MatTypeCV8UC1, bits)
	if err != nil {
		return nil, err
	}
	defer bitmap.Close()
	kernel := gocv.Ones(2, 2, gocv.MatTypeCV8UC1)
	defer kernel.Close()
	dilated := gocv.NewMat()
	defer dilated.Close()
	gocv.Dilate(bitmap, &dilated, kernel)
	contours := gocv.FindContours(dilated, gocv.RetrievalList, gocv.ChainApproxSimple)
	defer contours.Close()

	var quads [][8]float32
	for i := 0; i < min(contours.Size(), maxCandidates); i++ {
		rect := gocv.MinAreaRect2f(contours.At(i))
		if min(rect.Width, rect.Height) < minSide {
			continue
		}
		box := boxPoints(rect)
		if s, err := boxScore(prob, mw, mh, box); err != nil {
			return nil, err
		} else if s < boxThreshold {
			continue
		}
		grown, ok := unclip(rect, box)
		if !ok {
			continue
		}
		var q [4][2]float32
		for j, p := range grown {
			x := p.X * float32(srcW) / float32(mw)
			y := p.Y * float32(srcH) / float32(mh)
			q[j] = [2]float32{clamp(x, 0, float32(srcW-1)), clamp(y, 0, float32(srcH-1))}
		}
		quads = append(quads, orderQuad(q))
	}
	return quads, nil
}

// boxScore is the detector's mean probability inside box: the pixels
// cv2.fillPoly fills for its corners truncated to integers, as cv2.mean
// over that mask (0 for none). The mask covers just the box's bounding
// rectangle, not the whole map.
func boxScore(prob []float32, mw, mh int, box [4]gocv.Point2f) (float64, error) {
	pts := make([]image.Point, 4)
	x0, y0, x1, y1 := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for i, p := range box {
		pts[i] = image.Pt(int(p.X), int(p.Y)) // numpy's astype(int32): toward zero
		x0, y0 = min(x0, pts[i].X), min(y0, pts[i].Y)
		x1, y1 = max(x1, pts[i].X), max(y1, pts[i].Y)
	}
	x0, y0, x1, y1 = max(x0, 0), max(y0, 0), min(x1, mw-1), min(y1, mh-1)
	if x0 > x1 || y0 > y1 {
		return 0, nil
	}
	w, h := x1-x0+1, y1-y0+1
	for i := range pts {
		pts[i] = pts[i].Sub(image.Pt(x0, y0))
	}
	mask := gocv.Zeros(h, w, gocv.MatTypeCV8UC1)
	defer mask.Close()
	poly := gocv.NewPointsVectorFromPoints([][]image.Point{pts})
	defer poly.Close()
	gocv.FillPoly(&mask, poly, color.RGBA{255, 255, 255, 255})
	m, err := mask.DataPtrUint8()
	if err != nil {
		return 0, err
	}
	var sum float64
	n := 0
	for y := 0; y < h; y++ {
		row := prob[(y0+y)*mw+x0 : (y0+y)*mw+x0+w]
		for x, in := range m[y*w : (y+1)*w] {
			if in != 0 {
				sum += float64(row[x])
				n++
			}
		}
	}
	if n == 0 {
		return 0, nil
	}
	return sum * (1 / float64(n)), nil
}

// unclip is DB's unclip of a line's rectangle: grown by d =
// area*unclipRatio/perimeter on every side. PaddleOCR offsets the polygon
// with rounded corners (pyclipper) and takes the offset's minimum-area
// rectangle, which is this one - the same centre and angle, its sides 2d
// longer - up to pyclipper's integer coordinates: on the benchmark's
// samples the corners moved 0.8 px (median; 2.4 at most), and about 3% of
// the lines read a character differently, as often better as worse. ok is
// false when the grown rectangle is still thinner than minGrownSide.
func unclip(rect gocv.RotatedRect2f, box [4]gocv.Point2f) (grown [4]gocv.Point2f, ok bool) {
	area, length := areaPerimeter(box)
	d := area * unclipRatio / math.Max(length, 1e-6)
	w, h := float64(rect.Width)+2*d, float64(rect.Height)+2*d
	if math.Min(w, h) < minGrownSide {
		return grown, false
	}
	return boxPoints(gocv.RotatedRect2f{Center: rect.Center, Width: float32(w), Height: float32(h), Angle: rect.Angle}), true
}

// boxPoints is cv2.boxPoints: a rotated rectangle's corners. (gocv's
// MinAreaRect2f answers them too, but reads OpenCV's floats as integers.)
func boxPoints(r gocv.RotatedRect2f) [4]gocv.Point2f {
	m := gocv.NewMat()
	defer m.Close()
	r.Points = make([]gocv.Point2f, 4) // BoxPoints2f passes them along, unused
	gocv.BoxPoints2f(r, &m)
	var b [4]gocv.Point2f
	for i := range b {
		b[i] = gocv.Point2f{X: m.GetFloatAt(i, 0), Y: m.GetFloatAt(i, 1)}
	}
	return b
}

// areaPerimeter is cv2.contourArea and cv2.arcLength (closed) of four
// float corners, computed as OpenCV does.
func areaPerimeter(p [4]gocv.Point2f) (area, length float64) {
	prev := p[3]
	var a float64
	for _, q := range p {
		a += float64(prev.X)*float64(q.Y) - float64(prev.Y)*float64(q.X)
		dx, dy := q.X-prev.X, q.Y-prev.Y
		length += float64(float32(math.Sqrt(float64(dx*dx + dy*dy))))
		prev = q
	}
	return math.Abs(a * 0.5), length
}

// orderQuad is the corners top-left, top-right, bottom-right, bottom-left
// as x,y pairs: the least and greatest x+y, the least and greatest y-x (as
// the benchmark's crop_box).
func orderQuad(p [4][2]float32) [8]float32 {
	var s, d [4]float32
	for i, v := range p {
		s[i], d[i] = v[0]+v[1], v[1]-v[0]
	}
	tl, br, tr, bl := argmin(s), argmax(s), argmin(d), argmax(d)
	return [8]float32{p[tl][0], p[tl][1], p[tr][0], p[tr][1], p[br][0], p[br][1], p[bl][0], p[bl][1]}
}

// argmin and argmax are numpy's: the first of equals.
func argmin(v [4]float32) int {
	k := 0
	for i := 1; i < len(v); i++ {
		if v[i] < v[k] {
			k = i
		}
	}
	return k
}

func argmax(v [4]float32) int {
	k := 0
	for i := 1; i < len(v); i++ {
		if v[i] > v[k] {
			k = i
		}
	}
	return k
}

// sortQuads puts quads in reading order, as PaddleOCR's sorted_boxes: by
// their top-left corner's y, then x, then neighbours whose tops are within
// sameRow px of each other left to right.
func sortQuads(q [][8]float32) {
	sort.SliceStable(q, func(i, j int) bool {
		if q[i][1] != q[j][1] {
			return q[i][1] < q[j][1]
		}
		return q[i][0] < q[j][0]
	})
	for i := 0; i < len(q)-1; i++ {
		for j := i; j >= 0; j-- {
			if dy := q[j+1][1] - q[j][1]; dy < sameRow && dy > -sameRow && q[j+1][0] < q[j][0] {
				q[j], q[j+1] = q[j+1], q[j]
			} else {
				break
			}
		}
	}
}

func clamp(v, lo, hi float32) float32 {
	return min(max(v, lo), hi)
}
