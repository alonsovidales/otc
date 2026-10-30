// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesquality

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"testing"

	imagestagger "github.com/alonsovidales/otc/images_tagger"
)

func TestMeanScore(t *testing.T) {
	uniform := make([]float32, 10)
	for i := range uniform {
		uniform[i] = 0.1
	}
	if got, err := MeanScore(uniform); err != nil || math.Abs(float64(got)-5.5) > 1e-6 {
		t.Errorf("uniform: %v, %v; want 5.5", got, err)
	}
	// Not normalized: the probabilities are taken as their share.
	oneHot := []float32{0, 0, 0, 0, 0, 0, 0, 2, 0, 0}
	if got, err := MeanScore(oneHot); err != nil || got != 8 {
		t.Errorf("one hot on 8: %v, %v", got, err)
	}
	for name, p := range map[string][]float32{
		"short":    {1, 2, 3},
		"negative": {-1, 0, 0, 0, 0, 0, 0, 0, 0, 2},
		"none":     make([]float32, 10),
		"nan":      {float32(math.NaN()), 0, 0, 0, 0, 0, 0, 0, 0, 1},
	} {
		if _, err := MeanScore(p); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// coloured is a w x h picture whose pixels say where they are.
func coloured(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff})
		}
	}
	return img
}

// Resize takes the pixel under each target pixel's centre, as PIL's
// nearest-neighbour does: column x of 224 reads floor((x+0.5)*w/224).
func TestResizeNearestLikePIL(t *testing.T) {
	src := coloured(250, 120)
	got := Resize(src)
	if got.Rect != image.Rect(0, 0, InputSize, InputSize) {
		t.Fatalf("size %v", got.Rect)
	}
	for _, c := range [][2]int{{0, 0}, {1, 7}, {100, 50}, {223, 223}, {112, 111}} {
		x, y := c[0], c[1]
		sx := int((float64(x) + 0.5) * 250 / 224)
		sy := int((float64(y) + 0.5) * 120 / 224)
		if want, have := src.RGBAAt(sx, sy), got.RGBAAt(x, y); want != have {
			t.Errorf("(%d,%d): %v, want the source's (%d,%d) %v", x, y, have, sx, sy, want)
		}
	}
	// A JPEG's YCbCr reads as the same RGB its At gives.
	ycc := image.NewYCbCr(image.Rect(0, 0, 300, 200), image.YCbCrSubsampleRatio420)
	for i := range ycc.Y {
		ycc.Y[i] = uint8(i * 7)
	}
	for i := range ycc.Cb {
		ycc.Cb[i], ycc.Cr[i] = uint8(i*3), uint8(255-i)
	}
	r := Resize(ycc)
	fx, fy := 10.5*300/224.0, 20.5*200/224.0
	sx, sy := int(fx), int(fy)
	want := color.RGBAModel.Convert(ycc.At(sx, sy)).(color.RGBA)
	if have := r.RGBAAt(10, 20); have != want {
		t.Errorf("YCbCr: %v, want %v", have, want)
	}
}

func TestInputScalesToMinusOneOne(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, InputSize, InputSize))
	img.SetRGBA(0, 0, color.RGBA{0, 255, 128, 255})
	in := Input(img)
	if len(in) != 3*InputSize*InputSize {
		t.Fatalf("%d values", len(in))
	}
	if in[0] != -1 || in[1] != 1 || math.Abs(float64(in[2])-(128/127.5-1)) > 1e-6 {
		t.Errorf("first pixel %v", in[:3])
	}
}

func TestSharpness(t *testing.T) {
	flat := image.NewGray(image.Rect(0, 0, 64, 64))
	for i := range flat.Pix {
		flat.Pix[i] = 128
	}
	if s := Sharpness(flat); s != 0 {
		t.Errorf("flat: %v", s)
	}
	edges := image.NewGray(image.Rect(0, 0, 64, 64))
	soft := image.NewGray(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if (x/4+y/4)%2 == 0 {
				edges.Pix[y*64+x] = 255
			}
			// The same squares, blurred into a gentle wave.
			soft.Pix[y*64+x] = uint8(128 + 60*math.Sin(float64(x)/4)*math.Sin(float64(y)/4))
		}
	}
	if Sharpness(edges) <= 10*Sharpness(soft) {
		t.Errorf("sharp %v not well above soft %v", Sharpness(edges), Sharpness(soft))
	}
	if s := Sharpness(image.NewGray(image.Rect(0, 0, 2, 2))); s != 0 {
		t.Errorf("tiny: %v", s)
	}
}

func TestExposure(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for i := 0; i < 100; i++ {
		v := uint8(128)
		switch {
		case i < 30:
			v = 0
		case i < 50:
			v = 255
		}
		img.SetRGBA(i%10, i/10, color.RGBA{v, v, v, 255})
	}
	l := Exposure(img)
	if math.Abs(l.Dark-0.3) > 1e-9 || math.Abs(l.Bright-0.2) > 1e-9 {
		t.Errorf("%+v", l)
	}
	if want := (20*255.0 + 50*128) / 100; math.Abs(l.Mean-want) > 1 {
		t.Errorf("mean %v, want about %v", l.Mean, want)
	}
}

func TestDHash(t *testing.T) {
	a := coloured(400, 300)
	if d := Distance(DHash(a), DHash(a)); d != 0 {
		t.Errorf("itself: %d", d)
	}
	// The same scene a little brighter: a few bits at most.
	b := image.NewRGBA(a.Rect)
	for i := range a.Pix {
		b.Pix[i] = uint8(min(255, int(a.Pix[i])+6))
	}
	if d := Distance(DHash(a), DHash(b)); d > 8 {
		t.Errorf("brighter: %d bits", d)
	}
	// Turned round: a different picture.
	c := image.NewRGBA(a.Rect)
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			c.SetRGBA(x, y, a.RGBAAt(399-x, 299-y))
		}
	}
	if d := Distance(DHash(a), DHash(c)); d < 20 {
		t.Errorf("turned round: only %d bits", d)
	}
	if Distance(0, ^uint64(0)) != 64 {
		t.Error("distance")
	}
}

// The model itself, against the scores ONNX Runtime gives in Python for the
// same pictures. Runs when both are given:
//
//	OTC_TEST_NIMA_MODEL=/path/nima_mobilenet_aesthetic.onnx
//	OTC_TEST_ORT_LIB=/path/libonnxruntime.dylib (or .so)
//	OTC_TEST_NIMA_THUMB=a 1000 px JPEG thumbnail, OTC_TEST_NIMA_THUMB_SCORE=its score (optional)
func TestNIMAScoresAsInPython(t *testing.T) {
	model, lib := os.Getenv("OTC_TEST_NIMA_MODEL"), os.Getenv("OTC_TEST_ORT_LIB")
	if model == "" || lib == "" {
		t.Skip("OTC_TEST_NIMA_MODEL and OTC_TEST_ORT_LIB not set")
	}
	imagestagger.RuntimeLibrary = lib
	n, err := NewNIMA(model, true)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	img := image.NewRGBA(image.Rect(0, 0, InputSize, InputSize))
	for y := 0; y < InputSize; y++ {
		for x := 0; x < InputSize; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8((x*3 + y*5) % 256), uint8((x * 7) % 256), uint8((y * 11) % 256), 255})
		}
	}
	got, err := n.AestheticResized(context.Background(), img)
	if err != nil {
		t.Fatal(err)
	}
	if want := 6.465234118172804; math.Abs(float64(got)-want) > 1e-4 {
		t.Errorf("synthetic: %v, Python says %v", got, want)
	}
	if _, err := n.AestheticResized(context.Background(), image.NewRGBA(image.Rect(0, 0, 10, 10))); err == nil {
		t.Error("a 10x10 image was scored")
	}
	if path := os.Getenv("OTC_TEST_NIMA_THUMB"); path != "" {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		thumb, err := jpeg.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		got, err := n.AestheticResized(context.Background(), Resize(thumb))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %v", path, got)
		if s := os.Getenv("OTC_TEST_NIMA_THUMB_SCORE"); s != "" {
			var want float64
			if _, err := fmt.Sscan(s, &want); err != nil {
				t.Fatal(err)
			}
			// Go's JPEG decoder rounds a little differently from libjpeg.
			if math.Abs(float64(got)-want) > 0.05 {
				t.Errorf("%s: %v, Python says %v", path, got, want)
			}
		}
	}
}
