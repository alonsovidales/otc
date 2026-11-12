// SPDX-License-Identifier: AGPL-3.0-or-later

package textreader

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"

	"gocv.io/x/gocv"
)

// line is a line cut out of the photo for the recogniser.
type line struct {
	i    int        // its index in the detector's order
	quad [8]float32 // where it is in the photo
	w, h int        // the cut-out's size, upright
	rw   int        // its width at recHeight
	pix  []byte     // rw x recHeight R,G,B bytes
}

func (l line) ratio() float64 { return float64(l.w) / float64(l.h) }

// batchWidth is the width a batch whose widest line (by width/height) is l
// is padded to, as PaddleOCR's: that line's at recHeight, rounded up to a
// multiple of 8, within [recMinWidth, recMaxWidth].
func batchWidth(l line) int {
	w := int(math.Ceil(float64(recHeight*l.w)/float64(l.h)*1.0/8)) * 8
	return min(recMaxWidth, max(recMinWidth, w))
}

// cutMargin is the pixels around a quad's bounding rectangle cutLine reads:
// INTER_CUBIC reads one before and two after each sample, so the warp sees
// what it would in the whole photo.
const cutMargin = 3

// cutLine is the line at q cut out of img as the benchmark's crop_box and
// ocr_rec do: perspective-corrected (INTER_CUBIC, BORDER_REPLICATE) to the
// longer of its opposite sides, turned a quarter counter-clockwise when it
// is half again as tall as wide (vertical text), then resized to
// recHeight (INTER_LINEAR). Only the quad's surroundings are copied out of
// the photo. ok is false for a quad thinner than 2 px.
func cutLine(img image.Image, q [8]float32) (l line, ok bool, err error) {
	wd := int(max(dist(q[0], q[1], q[2], q[3]), dist(q[6], q[7], q[4], q[5])))
	ht := int(max(dist(q[0], q[1], q[6], q[7]), dist(q[2], q[3], q[4], q[5])))
	if wd < 2 || ht < 2 {
		return l, false, nil
	}
	b := img.Bounds()
	minX, minY, maxX, maxY := q[0], q[1], q[0], q[1]
	for i := 2; i < 8; i += 2 {
		minX, maxX = min(minX, q[i]), max(maxX, q[i])
		minY, maxY = min(minY, q[i+1]), max(maxY, q[i+1])
	}
	r := image.Rect(int(math.Floor(float64(minX)))-cutMargin, int(math.Floor(float64(minY)))-cutMargin,
		int(math.Floor(float64(maxX)))+cutMargin+1, int(math.Floor(float64(maxY)))+cutMargin+1).Add(b.Min).Intersect(b)
	if r.Empty() {
		return l, false, nil
	}
	src, err := region(img, r)
	if err != nil {
		return l, false, err
	}
	defer src.Close()

	// The quad in the region's pixels (exact: whole numbers off floats).
	ox, oy := float32(r.Min.X-b.Min.X), float32(r.Min.Y-b.Min.Y)
	from := gocv.NewPoint2fVectorFromPoints([]gocv.Point2f{
		{X: q[0] - ox, Y: q[1] - oy}, {X: q[2] - ox, Y: q[3] - oy}, {X: q[4] - ox, Y: q[5] - oy}, {X: q[6] - ox, Y: q[7] - oy}})
	defer from.Close()
	to := gocv.NewPoint2fVectorFromPoints([]gocv.Point2f{
		{X: 0, Y: 0}, {X: float32(wd), Y: 0}, {X: float32(wd), Y: float32(ht)}, {X: 0, Y: float32(ht)}})
	defer to.Close()
	m := gocv.GetPerspectiveTransform2f(from, to)
	defer m.Close()
	cut := gocv.NewMat()
	defer cut.Close()
	gocv.WarpPerspectiveWithParams(src, &cut, m, image.Pt(wd, ht), gocv.InterpolationCubic, gocv.BorderReplicate, color.RGBA{})
	upright := cut
	if float64(ht)*1.0/float64(wd) >= 1.5 {
		rot := gocv.NewMat()
		defer rot.Close()
		gocv.Rotate(cut, &rot, gocv.Rotate90CounterClockwise) // numpy's rot90
		upright = rot
	}

	l = line{quad: q, w: upright.Cols(), h: upright.Rows()}
	// Its width in its batch is this too: the batch is as wide as its
	// widest line by width/height, or recMaxWidth.
	l.rw = max(1, min(recMaxWidth, int(math.Ceil(float64(recHeight*l.w)/float64(l.h)))))
	small := gocv.NewMat()
	defer small.Close()
	gocv.Resize(upright, &small, image.Pt(l.rw, recHeight), 0, 0, gocv.InterpolationLinear)
	if small.Cols() != l.rw || small.Rows() != recHeight || small.Channels() != 3 {
		return l, false, fmt.Errorf("the line resized to %dx%dx%d", small.Cols(), small.Rows(), small.Channels()) // i18n-ignore: logged only
	}
	l.pix = small.ToBytes()
	return l, true, nil
}

// dist is the distance between two points, as numpy's float32 norm.
func dist(ax, ay, bx, by float32) float32 {
	dx, dy := bx-ax, by-ay
	return float32(math.Sqrt(float64(dx*dx + dy*dy)))
}

// region is img's pixels in r as an R,G,B Mat.
func region(img image.Image, r image.Rectangle) (gocv.Mat, error) {
	w := r.Dx()
	buf := make([]byte, w*r.Dy()*3)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		readRow(img, y, r.Min.X, r.Max.X, buf[(y-r.Min.Y)*w*3:])
	}
	return gocv.NewMatFromBytes(r.Dy(), w, gocv.MatTypeCV8UC3, buf)
}

// decode is greedy CTC over a line's steps x classes probabilities: each
// step's most likely class (the first of equals, as numpy's argmax),
// repeats merged and blanks (class 0) dropped. score is the mean
// probability of the characters kept, 0 for none.
func decode(p []float32, steps, classes int, dict []string) (text string, score float32) {
	var sb strings.Builder
	var sum float64
	n, last := 0, -1
	for t := 0; t < steps; t++ {
		row := p[t*classes : (t+1)*classes]
		k, best := 0, row[0]
		for c, v := range row {
			if v > best {
				k, best = c, v
			}
		}
		if k != last && k != 0 {
			if k < len(dict) {
				sb.WriteString(dict[k])
			}
			sum += float64(best)
			n++
		}
		last = k
	}
	if n == 0 {
		return "", 0
	}
	return sb.String(), float32(sum / float64(n))
}

// readDictionary is the recogniser's classes from its inference.yml.
func readDictionary(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dict := parseDictionary(string(b))
	if len(dict) <= 2 {
		return nil, errors.New(path + ": no character_dict") // i18n-ignore: logged only
	}
	return dict, nil
}

// parseDictionary is PaddleOCR's CTC classes from an inference.yml: the
// blank, the "  - " entries under "  character_dict:" (single- or
// double-quoted ones unquoted, a doubled single quote read as one), then a
// space - as the benchmark's rec_dict reads them.
func parseDictionary(yml string) []string {
	dict := []string{"<blank>"} // i18n-ignore: CTC's blank, never shown
	on := false
	for _, ln := range strings.Split(yml, "\n") {
		ln = strings.TrimSuffix(ln, "\r") // Python reads "\r\n" as "\n"
		if strings.HasPrefix(ln, "  character_dict:") {
			on = true
			continue
		}
		if !on {
			continue
		}
		v, isEntry := strings.CutPrefix(ln, "  - ")
		if !isEntry {
			break
		}
		if len(v) >= 2 && v[0] == v[len(v)-1] && (v[0] == '\'' || v[0] == '"') {
			v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
		dict = append(dict, v)
	}
	return append(dict, " ")
}
