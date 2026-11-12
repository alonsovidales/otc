// SPDX-License-Identifier: AGPL-3.0-or-later

package petfinder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"

	imagestagger "github.com/alonsovidales/otc/images_tagger"
)

// The model tests need the models, which are not in the repository:
// OTC_TEST_VISION_MODELS names a folder holding dfine-s-coco.onnx and
// dinov2-s-int8.onnx (text_reader's too), and OTC_ORT_LIB the ONNX Runtime
// library when it isn't at images_tagger.RuntimeLibrary. Without them
// those tests skip.
func modelFiles(t *testing.T) Files {
	t.Helper()
	dir := os.Getenv("OTC_TEST_VISION_MODELS")
	if dir == "" {
		t.Skip("OTC_TEST_VISION_MODELS is not set")
	}
	f := Files{Detector: filepath.Join(dir, "dfine-s-coco.onnx"), Embedder: filepath.Join(dir, "dinov2-s-int8.onnx")}
	for _, p := range []string{f.Detector, f.Embedder} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("no %s: %v", p, err)
		}
	}
	if lib := os.Getenv("OTC_ORT_LIB"); lib != "" {
		imagestagger.RuntimeLibrary = lib
	}
	if _, err := os.Stat(imagestagger.RuntimeLibrary); err != nil {
		t.Skipf("no ONNX Runtime at %s", imagestagger.RuntimeLibrary)
	}
	return f
}

func decodeFile(t *testing.T, name string) image.Image {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Find finds the reference's pets (testdata/reference.json: the Python
// benchmark's, from Pillow's decode) and describes them alike: Go decodes
// a JPEG a little differently from Pillow.
func TestFindMatchesReference(t *testing.T) {
	f, err := Load(modelFiles(t), LoadOptions{NoArena: true, Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Images map[string]struct {
			Size [2]int
			Pets []struct {
				Kind      Kind
				Score     float32
				Box       [4]float64
				Embedding []float32
			}
		}
	}
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	for name, want := range ref.Images {
		img := decodeFile(t, name)
		got, err := f.Find(context.Background(), img)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want.Pets) {
			t.Fatalf("%s: %d pets, the reference %d", name, len(got), len(want.Pets))
		}
		for i, w := range want.Pets {
			g := got[i]
			box := [4]float64{float64(g.X), float64(g.Y), float64(g.X + g.W), float64(g.Y + g.H)}
			var cos float64
			for j := range w.Embedding {
				cos += float64(g.Embedding[j]) * float64(w.Embedding[j])
			}
			t.Logf("%s: kind %d score %.3f (%.3f) IoU %.3f cosine %.4f", name, g.Kind, g.Score, w.Score, iou(box, w.Box), cos)
			if g.Kind != w.Kind || iou(box, w.Box) < 0.9 || math.Abs(float64(g.Score-w.Score)) > 0.05 {
				t.Errorf("%s: %v %v at %v, the reference %v %v at %v", name, g.Kind, g.Score, box, w.Kind, w.Score, w.Box)
			}
			if len(g.Embedding) != 384 || cos < 0.98 {
				t.Errorf("%s: embedding of %d, cosine %.4f with the reference's", name, len(g.Embedding), cos)
			}
			th, err := jpeg.Decode(bytes.NewReader(g.Thumbnail))
			if err != nil || th.Bounds().Size() != image.Pt(ThumbnailSize, ThumbnailSize) {
				t.Errorf("%s: thumbnail %v, %v", name, th.Bounds(), err)
			}
		}
	}
}

func TestFindCancelledAndClosed(t *testing.T) {
	fs := modelFiles(t)
	f, err := Load(fs, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	img := decodeFile(t, "collie.jpg")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Find(ctx, img); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	f.Close()
	if _, err := f.Find(context.Background(), img); err == nil {
		t.Fatal("found after Close")
	}
	f.Close()

	swapped := Files{Detector: fs.Embedder, Embedder: fs.Detector}
	if f, err := Load(swapped, LoadOptions{}); err == nil {
		f.Close()
		t.Fatal("loaded the embedder as the detector")
	}
}

// A photo without pets has none.
func TestFindNone(t *testing.T) {
	f, err := Load(modelFiles(t), LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img := image.NewGray(image.Rect(0, 0, 300, 200))
	for i := range img.Pix {
		img.Pix[i] = byte(i % 7 * 30)
	}
	pets, err := f.Find(context.Background(), img)
	if err != nil || len(pets) != 0 {
		t.Fatal(pets, err)
	}
}
