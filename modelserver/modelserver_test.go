// SPDX-License-Identifier: AGPL-3.0-or-later

package modelserver

import (
	"context"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	facerecognition "github.com/alonsovidales/otc/face_recognition"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
)

type fakeTagger struct {
	gotW, gotH int
	gotPix     color.RGBA
}

func (f *fakeTagger) Tags(_ context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	f.gotW, f.gotH = img.Bounds().Dx(), img.Bounds().Dy()
	f.gotPix = img.At(3, 2).(color.RGBA)
	return []imagestagger.RAMTag{{Name: "dog", Score: 0.9}}, nil
}

type fakeFaces struct{}

func (fakeFaces) DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error) {
	return []facerecognition.FaceDetection{{X: 1, Y: 2, W: 30, H: 40, Score: 0.8, Embedding: []float32{0.1, 0.2}, Thumbnail: []byte{9}}}, nil
}

func serveForTest(t *testing.T, faces FaceDetector) (*Client, *fakeTagger) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "otcms") // short: Unix socket paths are length-limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "m.sock")
	tg := &fakeTagger{}
	go Serve(path, func() Tagger { return tg }, faces)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &Client{Path: path}, tg
}

// Issue #167: a child's image reaches the primary's model whole - full
// resolution, same pixels - and the results come back as they were.
func TestClientUsesTheSharedModels(t *testing.T) {
	c, tg := serveForTest(t, fakeFaces{})
	img := image.NewNRGBA(image.Rect(10, 10, 4042, 3034)) // not RGBA, not at 0,0
	img.Set(13, 12, color.NRGBA{R: 200, G: 100, B: 50, A: 255})

	tags, err := c.Tags(context.Background(), img, imagestagger.RAMOptions{ImageSize: 384})
	if err != nil || len(tags) != 1 || tags[0].Name != "dog" {
		t.Fatalf("tags %v %v", tags, err)
	}
	if tg.gotW != 4032 || tg.gotH != 3024 {
		t.Errorf("the model got %dx%d, want the full 4032x3024", tg.gotW, tg.gotH)
	}
	if tg.gotPix != (color.RGBA{R: 200, G: 100, B: 50, A: 255}) {
		t.Errorf("pixel changed on the way: %v", tg.gotPix)
	}

	faces, err := c.DetectFaces(img)
	if err != nil || len(faces) != 1 || faces[0].W != 30 || len(faces[0].Embedding) != 2 || faces[0].Thumbnail[0] != 9 {
		t.Fatalf("faces %+v %v", faces, err)
	}
	if has, err := c.HasFaces(); err != nil || !has {
		t.Errorf("HasFaces = %v %v", has, err)
	}
}

func TestNoFaceModelsOnThePrimary(t *testing.T) {
	c, _ := serveForTest(t, nil)
	if has, err := c.HasFaces(); err != nil || has {
		t.Errorf("HasFaces = %v %v, want false", has, err)
	}
	if _, err := c.DetectFaces(image.NewRGBA(image.Rect(0, 0, 2, 2))); err == nil {
		t.Error("faces answered without face models")
	}
}
