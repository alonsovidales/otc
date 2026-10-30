// SPDX-License-Identifier: AGPL-3.0-or-later

package modelserver

import (
	"context"
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"

	imagesembedder "github.com/alonsovidales/otc/images_embedder"
)

type fakeEmbedder struct {
	img  *image.RGBA
	text string
}

func (f *fakeEmbedder) EmbedResized(_ context.Context, img *image.RGBA) ([]float32, error) {
	f.img = img
	return []float32{0.6, 0.8}, nil
}

func (f *fakeEmbedder) EmbedText(_ context.Context, text string) ([]float32, error) {
	f.text = text
	return []float32{1, 0}, nil
}

// Smart search: a child's photos and searches are described by the
// primary's model, from the 256x256 picture the child resized itself.
func TestClientEmbedsWithThePrimarysModel(t *testing.T) {
	path := socketPath(t)
	fe := &fakeEmbedder{}
	go ServeModels(path, Models{Tagger: func() Tagger { return &fakeTagger{} }, Embedder: fe}, 2)
	waitForSocket(path)
	c := &Client{Path: path}
	if has, err := c.HasEmbedder(); err != nil || !has {
		t.Fatalf("HasEmbedder = %v %v", has, err)
	}
	img := imagesembedder.Resize(image.NewRGBA(image.Rect(0, 0, 1000, 750)))
	img.SetRGBA(9, 4, color.RGBA{R: 7, G: 8, B: 9, A: 255})
	vec, err := c.EmbedResized(context.Background(), img)
	if err != nil || len(vec) != 2 || vec[1] != 0.8 {
		t.Fatalf("image vector %v %v", vec, err)
	}
	if fe.img == nil || fe.img.Rect.Dx() != imagesembedder.ImageSize || fe.img.RGBAAt(9, 4) != (color.RGBA{R: 7, G: 8, B: 9, A: 255}) {
		t.Error("the model didn't get the child's picture")
	}
	if vec, err := c.EmbedText(context.Background(), "un perro en la playa"); err != nil || len(vec) != 2 || vec[0] != 1 {
		t.Fatalf("text vector %v %v", vec, err)
	}
	if fe.text != "un perro en la playa" {
		t.Errorf("the model got %q", fe.text)
	}
	// Anything but the model's square, or a text past the socket's limit,
	// is refused before the model.
	if _, err := c.EmbedResized(context.Background(), image.NewRGBA(image.Rect(0, 0, 100, 100))); err == nil {
		t.Error("a 100x100 picture was described")
	}
	if _, err := c.EmbedText(context.Background(), strings.Repeat("a", cMaxEmbedText+1)); err == nil {
		t.Error("an over-long text was described")
	}
}

// A primary without the model, and one older than it, answer ErrNoEmbedder:
// the child can't turn Smart search on.
func TestNoEmbedderReachesTheChildAsItself(t *testing.T) {
	path := socketPath(t)
	go ServeModels(path, Models{Tagger: func() Tagger { return &fakeTagger{} }}, 0)
	waitForSocket(path)
	c := &Client{Path: path}
	if has, err := c.HasEmbedder(); err != nil || has {
		t.Errorf("HasEmbedder = %v %v, want false", has, err)
	}
	img := imagesembedder.Resize(image.NewRGBA(image.Rect(0, 0, 10, 10)))
	if _, err := c.EmbedResized(context.Background(), img); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("no model, image: %v", err)
	}
	if _, err := c.EmbedText(context.Background(), "snow"); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("no model, text: %v", err)
	}
	old := &Client{Path: oldPrimary(t, make(chan request, 4))}
	if has, err := old.HasEmbedder(); err != nil || has {
		t.Errorf("an older primary: HasEmbedder = %v %v", has, err)
	}
	if _, err := old.EmbedText(context.Background(), "snow"); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("an older primary: %v", err)
	}
}
