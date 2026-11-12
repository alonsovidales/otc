// SPDX-License-Identifier: AGPL-3.0-or-later

package textreader

import (
	"bytes"
	"encoding/gob"
	"image"
	"image/color"
	"math"
	"reflect"
	"testing"

	"gocv.io/x/gocv"
)

func TestParseDictionary(t *testing.T) {
	yml := "Global:\n  model_name: PP-OCRv6_small_rec\nPostProcess:\n  name: CTCLabelDecode\n  character_dict:\n" +
		"  - '!'\n  - '\"'\n  - ''''\n  - $\n  - x\n  - \\\n  - 中\r\n  - '''a'''\n  - \"b\"\nOther:\n  - y\n"
	got := parseDictionary(yml)
	want := []string{"<blank>", "!", "\"", "'", "$", "x", "\\", "中", "'a'", "b", " "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
	if got := parseDictionary("a: b\n"); len(got) != 2 {
		t.Fatalf("no character_dict: %q", got)
	}
}

func TestDecode(t *testing.T) {
	dict := []string{"<blank>", "a", "b", "c"}
	const classes = 4
	steps := []struct {
		class int
		p     float32
	}{{1, 0.9}, {1, 0.8}, {0, 0.7}, {1, 0.6}, {2, 0.95}, {2, 0.5}, {0, 0.99}, {3, 0.4}, {3, 0.4}}
	probs := make([]float32, len(steps)*classes)
	for i, s := range steps {
		for c := 0; c < classes; c++ {
			probs[i*classes+c] = (1 - s.p) / (classes - 1)
		}
		probs[i*classes+s.class] = s.p
	}
	text, score := decode(probs, len(steps), classes, dict)
	// repeats merged, blanks dropped, a repeat after a blank kept
	if text != "aabc" {
		t.Fatalf("text %q, want aabc", text)
	}
	if want := float32((0.9 + 0.6 + 0.95 + 0.4) / 4); math.Abs(float64(score-want)) > 1e-6 {
		t.Fatalf("score %v, want %v", score, want)
	}
	// Ties go to the first class, as numpy's argmax.
	tie := []float32{0.25, 0.25, 0.25, 0.25}
	if text, score := decode(tie, 1, classes, dict); text != "" || score != 0 {
		t.Fatalf("a tied step read %q (%v)", text, score)
	}
	// A class past the dictionary reads as nothing but counts.
	if text, _ := decode([]float32{0, 0, 0, 0, 1}, 1, 5, dict); text != "" {
		t.Fatalf("class past the dictionary read %q", text)
	}
}

func TestDetSize(t *testing.T) {
	for _, c := range []struct{ w, h, dw, dh int }{
		{4032, 3024, 992, 736},  // a phone photo: short side 736
		{1500, 1000, 1088, 736}, // 1104/32 = 34.5 rounds to even, as Python's round
		{1000, 1500, 736, 1088},
		{640, 480, 640, 480},    // never scaled up
		{500, 10000, 192, 4000}, // the long side capped
		{20, 10, 32, 32},        // at least 32
		{1224, 1584, 736, 960},  // hf_sample.png, as the benchmark
		{1896, 215, 1888, 224},  // a banner
		{360, 270, 352, 256},    // 270/32 = 8.4375 -> 8
		{2400, 1751, 1024, 736}, // the demo's photos
	} {
		if dw, dh := detSize(c.w, c.h); dw != c.dw || dh != c.dh {
			t.Errorf("%dx%d: %dx%d, want %dx%d", c.w, c.h, dw, dh, c.dw, c.dh)
		}
	}
}

func TestDetPixels(t *testing.T) {
	got := detPixels([]byte{255, 0, 10, 0, 128, 255})
	want := []float32{
		float32((float64(float32(10)/255) - 0.485) / 0.229), float32((1 - 0.485) / 0.229), // B
		float32((0 - 0.456) / 0.224), float32((float64(float32(128)/255) - 0.456) / 0.224), // G
		float32((1 - 0.406) / 0.225), float32((0 - 0.406) / 0.225), // R
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

func TestOrderQuad(t *testing.T) {
	// A slightly rotated box's corners in boxPoints' order (bottom-left first).
	got := orderQuad([4][2]float32{{10, 52}, {12, 30}, {110, 38}, {108, 60}})
	want := [8]float32{12, 30, 110, 38, 108, 60, 10, 52}
	if got != want {
		t.Fatalf("%v, want %v", got, want)
	}
}

func TestSortQuads(t *testing.T) {
	quad := func(x, y float32) [8]float32 { return [8]float32{x, y, x + 50, y, x + 50, y + 20, x, y + 20} }
	q := [][8]float32{quad(300, 105), quad(10, 400), quad(200, 100), quad(20, 108), quad(100, 300), quad(5, 300)}
	sortQuads(q)
	// The first row (tops within 10 px) left to right, then the others by y.
	want := [][8]float32{quad(20, 108), quad(200, 100), quad(300, 105), quad(5, 300), quad(100, 300), quad(10, 400)}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("%v\nwant %v", q, want)
	}
}

// The unclipped rectangle is the minimum-area rectangle of the box offset
// with round corners, in whole pixels as pyclipper's - what PaddleOCR
// takes.
func TestUnclipIsTheRoundOffsetsRectangle(t *testing.T) {
	for _, rect := range []gocv.RotatedRect2f{
		{Center: gocv.Point2f{X: 300.3, Y: 200.7}, Width: 180.4, Height: 36.2, Angle: 7.5},
		{Center: gocv.Point2f{X: 120, Y: 80}, Width: 22, Height: 64.5, Angle: 90},
		{Center: gocv.Point2f{X: 500.5, Y: 400.25}, Width: 400, Height: 12, Angle: -30},
	} {
		box := boxPoints(rect)
		grown, ok := unclip(rect, box)
		if !ok {
			t.Fatal("not grown")
		}
		area, length := areaPerimeter(box)
		d := area * unclipRatio / length
		// The offset: each edge pushed out by d, the corners arcs of radius d.
		var poly []image.Point
		for i := 0; i < 4; i++ {
			a, b := box[i], box[(i+1)%4]
			ex, ey := float64(b.X-a.X), float64(b.Y-a.Y)
			n := math.Hypot(ex, ey)
			// The outward normal (the corners go round clockwise or not).
			nx, ny := ey/n, -ex/n
			cx, cy := float64(rect.Center.X), float64(rect.Center.Y)
			if (float64(a.X)-cx)*nx+(float64(a.Y)-cy)*ny < 0 {
				nx, ny = -nx, -ny
			}
			start := math.Atan2(ny, nx)
			for s := -45; s <= 45; s++ { // the arc round corner a, then the edge
				th := start + float64(s)*math.Pi/180
				poly = append(poly, image.Pt(int(math.Round(float64(a.X)+d*math.Cos(th))), int(math.Round(float64(a.Y)+d*math.Sin(th)))))
				poly = append(poly, image.Pt(int(math.Round(float64(b.X)+d*math.Cos(th))), int(math.Round(float64(b.Y)+d*math.Sin(th)))))
			}
		}
		pv := gocv.NewPointVectorFromPoints(poly)
		wantBox := boxPoints(gocv.MinAreaRect2f(pv))
		pv.Close()
		for _, p := range grown {
			best := math.Inf(1)
			for _, q := range wantBox {
				best = math.Min(best, math.Hypot(float64(p.X-q.X), float64(p.Y-q.Y)))
			}
			if best > 1.0 {
				t.Errorf("%v: grown corner %v is %.2f px from the offset's rectangle %v", rect, p, best, wantBox)
			}
		}
	}
}

func TestDBBoxes(t *testing.T) {
	const mw, mh = 200, 100
	prob := make([]float32, mw*mh)
	fill := func(x0, y0, x1, y1 int, p float32) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				prob[y*mw+x] = p
			}
		}
	}
	fill(40, 30, 140, 50, 0.9)  // a line
	fill(10, 70, 60, 90, 0.3)   // over the threshold, but not sure enough
	fill(100, 80, 180, 82, 0.9) // too thin
	quads, err := dbBoxes(prob, mw, mh, 2*mw, 2*mh)
	if err != nil {
		t.Fatal(err)
	}
	if len(quads) != 1 {
		t.Fatalf("%d lines, want 1: %v", len(quads), quads)
	}
	// Dilated by one pixel right and down, the region's corners are
	// (40, 30) and (140, 50): 100 x 20, grown by d = 2000*1.4/240 on every
	// side, then twice as big in the photo.
	d := 2000 * 1.4 / 240.0
	want := [8]float64{40 - d, 30 - d, 140 + d, 30 - d, 140 + d, 50 + d, 40 - d, 50 + d}
	for i, v := range quads[0] {
		if math.Abs(float64(v)-2*want[i]) > 0.01 {
			t.Fatalf("quad %v, want twice %v", quads[0], want)
		}
	}

	// Clipped to the photo.
	fill(0, 0, mw, mh, 0)
	fill(0, 0, 60, 12, 0.95)
	quads, err = dbBoxes(prob, mw, mh, mw, mh)
	if err != nil || len(quads) != 1 {
		t.Fatal(quads, err)
	}
	if q := quads[0]; q[0] != 0 || q[1] != 0 || q[2] > mw-1 {
		t.Fatalf("not clipped: %v", q)
	}
}

func TestBatchWidth(t *testing.T) {
	for _, c := range []struct{ w, h, want int }{
		{100, 48, 104}, // 100 rounds up to a multiple of 8
		{200, 96, 104},
		{10, 48, 32}, // at least 32
		{10000, 48, 3200},
		{96, 48, 96},
	} {
		if got := batchWidth(line{w: c.w, h: c.h}); got != c.want {
			t.Errorf("%dx%d: %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

func TestCutLine(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7 % 251)
	}
	// A level line exactly 48 px high comes out as the photo's pixels.
	l, ok, err := cutLine(img, [8]float32{20, 30, 120, 30, 120, 78, 20, 78})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if l.w != 100 || l.h != 48 || l.rw != 100 {
		t.Fatalf("%dx%d at %d", l.w, l.h, l.rw)
	}
	for y := 0; y < 48; y++ {
		for x := 0; x < 100; x++ {
			c := img.NRGBAAt(20+x, 30+y)
			if p := l.pix[(y*100+x)*3:]; p[0] != c.R || p[1] != c.G || p[2] != c.B {
				t.Fatalf("(%d, %d): %v, want %v", x, y, p[:3], c)
			}
		}
	}

	// A tall one is turned a quarter counter-clockwise: its top-right
	// corner comes first.
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for y := 50; y < 54; y++ {
		for x := 226; x < 230; x++ {
			img.SetNRGBA(x, y, color.NRGBA{0xff, 0, 0, 0xff})
		}
	}
	l, ok, err = cutLine(img, [8]float32{200, 50, 230, 50, 230, 140, 200, 140})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if l.w != 90 || l.h != 30 || l.rw != 144 {
		t.Fatalf("%dx%d at %d", l.w, l.h, l.rw)
	}
	if p := l.pix[:3]; p[0] < 200 || p[1] > 100 {
		t.Fatalf("top-left %v, want red", p)
	}

	// A sliver is no line.
	if _, ok, _ := cutLine(img, [8]float32{10, 10, 60, 10, 60, 11, 10, 11}); ok {
		t.Fatal("a 1 px line was cut")
	}
}

// Lines go between processes (modelserver) by gob.
func TestLineGob(t *testing.T) {
	in := []Line{{Text: "Grüße 中文", Score: 0.93, Quad: [8]float32{1, 2, 3, 4, 5, 6, 7, 8}}}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(in); err != nil {
		t.Fatal(err)
	}
	var out []Line
	if err := gob.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("%v, want %v", out, in)
	}
}
