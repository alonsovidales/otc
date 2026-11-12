// SPDX-License-Identifier: AGPL-3.0-or-later

package petfinder

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"reflect"
	"testing"
)

// logit is the inverse of sigmoid.
func logit(p float64) float32 { return float32(math.Log(p / (1 - p))) }

// detection is D-FINE's answer for boxes (centre x, centre y, width,
// height as fractions) with a cat and a dog probability each.
func detection(classes int, rows ...[6]float64) (logits, boxes []float32) {
	for _, r := range rows {
		l := make([]float32, classes)
		for c := range l {
			l[c] = logit(0.01)
		}
		l[catClass], l[dogClass] = logit(r[4]), logit(r[5])
		logits = append(logits, l...)
		boxes = append(boxes, float32(r[0]), float32(r[1]), float32(r[2]), float32(r[3]))
	}
	return logits, boxes
}

func TestCandidates(t *testing.T) {
	const w, h = 1000, 500
	logits, boxes := detection(80,
		[6]float64{0.5, 0.5, 0.2, 0.4, 0.9, 0.1},  // a cat
		[6]float64{0.2, 0.3, 0.1, 0.2, 0.2, 0.55}, // a dog
		[6]float64{0.5, 0.5, 0.2, 0.2, 0.3, 0.45}, // neither
		[6]float64{0.95, 0.5, 0.2, 0.4, 0.6, 0.7}, // both, past the right edge
		[6]float64{0.5, 0.5, 0.2, 0.4, 0.5, 0.01}, // exactly 0.5
	)
	got := candidates(logits, boxes, 80, w, h)
	type c struct {
		kind Kind
		box  [4]float64
	}
	want := []c{
		{Cat, [4]float64{400, 150, 600, 350}},
		{Dog, [4]float64{150, 100, 250, 200}},
		{Cat, [4]float64{850, 150, 1000, 350}},
		{Dog, [4]float64{850, 150, 1000, 350}},
		{Cat, [4]float64{400, 150, 600, 350}},
	}
	if len(got) != len(want) {
		t.Fatalf("%d candidates, want %d: %v", len(got), len(want), got)
	}
	for i, g := range got {
		if g.kind != want[i].kind {
			t.Errorf("%d: kind %d, want %d", i, g.kind, want[i].kind)
		}
		for j := range g.box {
			if math.Abs(g.box[j]-want[i].box[j]) > 1e-3 {
				t.Errorf("%d: box %v, want %v", i, g.box, want[i].box)
				break
			}
		}
	}
	if math.Abs(float64(got[0].score)-0.9) > 1e-6 {
		t.Errorf("score %v, want 0.9", got[0].score)
	}
}

func TestChoose(t *testing.T) {
	box := func(x, y, s float64) [4]float64 { return [4]float64{x, y, x + s, y + s} }
	c := []candidate{
		{Dog, 0.6, box(100, 100, 200)},
		{Cat, 0.8, box(120, 110, 200)}, // the same animal as the dog, likelier
		{Dog, 0.7, box(500, 100, 20)},  // too small (32 px)
		{Dog, 0.9, box(700, 400, 40)},  // too small (5% of 1000)
		{Cat, 0.55, box(400, 600, 300)},
	}
	got := choose(c, 2000, 1000)
	want := []candidate{{Cat, 0.8, box(120, 110, 200)}, {Cat, 0.55, box(400, 600, 300)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}

	// At most maxPets, the likeliest.
	var many []candidate
	for i := 0; i < 15; i++ {
		many = append(many, candidate{Dog, float32(0.5 + 0.01*float64(i)), box(float64(i)*100, 0, 60)})
	}
	got = choose(many, 2000, 1000)
	if len(got) != maxPets || got[0].score != float32(0.5+0.01*14) || got[maxPets-1].score != float32(0.5+0.01*5) {
		t.Fatalf("%d pets: %v", len(got), got)
	}
}

func TestIoU(t *testing.T) {
	a := [4]float64{0, 0, 10, 10}
	for _, c := range []struct {
		b    [4]float64
		want float64
	}{
		{[4]float64{0, 0, 10, 10}, 1},
		{[4]float64{5, 0, 15, 10}, 50.0 / 150},
		{[4]float64{20, 20, 30, 30}, 0},
		{[4]float64{0, 0, 0, 0}, 0},
	} {
		if got := iou(a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%v: %v, want %v", c.b, got, c.want)
		}
	}
}

func TestCropBox(t *testing.T) {
	// Grown by 10% on every side, within the photo, rounded half to even
	// as Pillow's crop.
	if got, want := cropBox([4]float64{10.5, 20.25, 110.5, 120.25}, 1000, 1000), image.Rect(0, 10, 120, 130); got != want {
		t.Errorf("%v, want %v", got, want)
	}
	if got, want := cropBox([4]float64{900, 0, 1000, 50}, 1000, 800), image.Rect(890, 0, 1000, 55); got != want {
		t.Errorf("%v, want %v", got, want)
	}
}

func TestThumbSquare(t *testing.T) {
	for _, c := range []struct {
		x, y, bw, bh, w, h int
		want               image.Rectangle
	}{
		{100, 100, 200, 100, 1000, 800, image.Rect(100, 50, 300, 250)},   // centred
		{0, 10, 100, 300, 1000, 800, image.Rect(0, 10, 300, 310)},        // moved right, inside
		{900, 700, 100, 100, 1000, 800, image.Rect(900, 700, 1000, 800)}, // at the corner
		{850, 0, 150, 60, 1000, 800, image.Rect(850, 0, 1000, 150)},      // moved down, inside
		{0, 0, 600, 400, 600, 400, image.Rect(100, 0, 500, 400)},         // the photo's shorter side
		{10, 10, 31, 20, 1000, 800, image.Rect(10, 4, 41, 35)},           // an odd side
	} {
		if got := thumbSquare(c.x, c.y, c.bw, c.bh, c.w, c.h); got != c.want {
			t.Errorf("%+v: %v, want %v", c, got, c.want)
		}
	}
}

// The thumbnail is a ThumbnailSize JPEG in the photo's colours (R,G,B in
// order).
func TestThumbnail(t *testing.T) {
	img := image.NewNRGBA(image.Rect(10, 20, 410, 320))
	for y := 20; y < 320; y++ {
		for x := 10; x < 410; x++ {
			c := color.NRGBA{200, 30, 40, 0xff}
			if x >= 210 {
				c = color.NRGBA{20, 40, 220, 0xff}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	b, err := thumbnail(img, image.Rect(100, 0, 300, 200)) // relative to the bounds: half red, half blue
	if err != nil {
		t.Fatal(err)
	}
	th, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if s := th.Bounds().Size(); s != image.Pt(ThumbnailSize, ThumbnailSize) {
		t.Fatalf("%v", s)
	}
	near := func(c color.Color, r, g, b int) bool {
		cr, cg, cb, _ := c.RGBA()
		return math.Abs(float64(cr>>8)-float64(r)) < 12 && math.Abs(float64(cg>>8)-float64(g)) < 12 && math.Abs(float64(cb>>8)-float64(b)) < 12
	}
	if c := th.At(20, 80); !near(c, 200, 30, 40) {
		t.Errorf("left %v, want red", c)
	}
	if c := th.At(140, 80); !near(c, 20, 40, 220) {
		t.Errorf("right %v, want blue", c)
	}
	if b, err := thumbnail(img, image.Rectangle{}); b != nil || err != nil {
		t.Errorf("empty square: %v, %v", b, err)
	}
}

func TestPixels(t *testing.T) {
	rgb := []byte{255, 0, 10, 0, 128, 255}
	if got, want := detPixels(rgb), []float32{1, 0, 0, 128.0 / 255, 10.0 / 255, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("detector: %v, want %v", got, want)
	}
	norm := func(v byte, c int) float32 { return (float32(v)/255 - embMean[c]) / embStd[c] }
	want := []float32{norm(255, 0), norm(0, 0), norm(0, 1), norm(128, 1), norm(10, 2), norm(255, 2)}
	if got := embPixels(rgb); !reflect.DeepEqual(got, want) {
		t.Errorf("embedder: %v, want %v", got, want)
	}
}

func TestNormalize(t *testing.T) {
	v := []float32{3, 4}
	if !normalize(v) || math.Abs(float64(v[0])-0.6) > 1e-7 || math.Abs(float64(v[1])-0.8) > 1e-7 {
		t.Fatal(v)
	}
	if normalize([]float32{0, 0}) {
		t.Fatal("a zero vector normalised")
	}
}

// Pets go between processes (modelserver) by gob.
func TestPetGob(t *testing.T) {
	in := []Pet{{Kind: Cat, Score: 0.7, X: 1, Y: 2, W: 3, H: 4, Embedding: []float32{0.6, 0.8}, Thumbnail: []byte{0xff, 0xd8}}}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(in); err != nil {
		t.Fatal(err)
	}
	var out []Pet
	if err := gob.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("%v, want %v", out, in)
	}
}

// The resize is Pillow's to the byte (testdata/pillow_resize.json, as
// text_reader's).
func TestResizeMatchesPillow(t *testing.T) {
	b, err := os.ReadFile("testdata/pillow_resize.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		W, H  int
		Cases []struct {
			Filter string
			Crop   [4]int
			Size   [2]int
			Sha256 string
		}
	}
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, ref.W, ref.H))
	for y := 0; y < ref.H; y++ {
		for x := 0; x < ref.W; x++ {
			i := img.PixOffset(x, y)
			for c := 0; c < 3; c++ {
				img.Pix[i+c] = uint8((x*x*7 + y*y*3 + x*y*5 + c*85 + (x^y)*11) % 256)
			}
			img.Pix[i+3] = 0xff
		}
	}
	for _, c := range ref.Cases {
		f := map[string]filter{"bilinear": bilinear, "bicubic": bicubic}[c.Filter]
		sum := sha256.Sum256(resizeRGB(img, image.Rect(c.Crop[0], c.Crop[1], c.Crop[2], c.Crop[3]), c.Size[0], c.Size[1], f))
		if hex.EncodeToString(sum[:]) != c.Sha256 {
			t.Errorf("%s %v -> %v: not Pillow's bytes", c.Filter, c.Crop, c.Size)
		}
	}
}
