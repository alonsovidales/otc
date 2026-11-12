// SPDX-License-Identifier: AGPL-3.0-or-later

package modelserver

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	petfinder "github.com/alonsovidales/otc/pet_finder"
	textreader "github.com/alonsovidales/otc/text_reader"
)

type fakeVision struct{ img image.Image }

func (f *fakeVision) ReadText(_ context.Context, img image.Image) ([]textreader.Line, error) {
	f.img = img
	return []textreader.Line{{Text: "GTS-04-R", Score: 0.97, Quad: [8]float32{1, 2, 3, 4, 5, 6, 7, 8}}}, nil
}

func (f *fakeVision) FindPets(_ context.Context, img image.Image) (petfinder.Found, error) {
	f.img = img
	return petfinder.Found{
		Pets:   []petfinder.Pet{{Kind: petfinder.Dog, Score: 0.9, X: 1, Y: 2, W: 30, H: 40, Embedding: []float32{0.6, 0.8}, Thumbnail: []byte{0xff, 0xd8}}},
		People: []image.Rectangle{image.Rect(5, 6, 20, 40)},
	}, nil
}

// Text in photos and Pets: a child's photo is read and looked at by the
// primary's models, from the picture the child sends, and the answers come
// back whole.
func TestClientReadsAndFindsWithThePrimarysModels(t *testing.T) {
	path := socketPath(t)
	fv := &fakeVision{}
	go ServeModels(path, Models{Tagger: func() Tagger { return &fakeTagger{} }, Vision: func() Vision { return fv }}, 2)
	waitForSocket(path)
	c := &Client{Path: path}
	if has, _, err := c.VisionStatus(); err != nil || !has {
		t.Fatalf("VisionStatus = %v %v", has, err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	img.SetRGBA(9, 4, color.RGBA{R: 7, G: 8, B: 9, A: 255})
	lines, err := c.ReadText(context.Background(), img)
	if err != nil || len(lines) != 1 || lines[0].Text != "GTS-04-R" || lines[0].Quad[7] != 8 {
		t.Fatalf("ReadText = %v %v", lines, err)
	}
	got, ok := fv.img.(*image.RGBA)
	if !ok || got.Rect.Dx() != 64 || got.RGBAAt(9, 4) != (color.RGBA{R: 7, G: 8, B: 9, A: 255}) {
		t.Error("the model didn't get the child's picture")
	}
	found, err := c.FindPets(context.Background(), img)
	pets := found.Pets
	if err != nil || len(pets) != 1 || pets[0].Kind != petfinder.Dog || pets[0].H != 40 || len(pets[0].Embedding) != 2 || len(pets[0].Thumbnail) != 2 {
		t.Fatalf("FindPets = %+v %v", found, err)
	}
	if len(found.People) != 1 || found.People[0] != image.Rect(5, 6, 20, 40) {
		t.Errorf("the people seen came back as %v", found.People)
	}
}

// A primary without the models, and one older than them, answer
// ErrNoVision: the child's photos wait for them.
func TestNoVisionReachesTheChildAsItself(t *testing.T) {
	path := socketPath(t)
	go ServeModels(path, Models{Tagger: func() Tagger { return &fakeTagger{} }}, 0)
	waitForSocket(path)
	c := &Client{Path: path}
	if has, _, err := c.VisionStatus(); err != nil || has {
		t.Errorf("VisionStatus = %v %v, want false", has, err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	if _, err := c.ReadText(context.Background(), img); !errors.Is(err, ErrNoVision) {
		t.Errorf("no models, text: %v", err)
	}
	if _, err := c.FindPets(context.Background(), img); !errors.Is(err, ErrNoVision) {
		t.Errorf("no models, pets: %v", err)
	}
	if _, _, err := c.FetchVision("u1", true); !errors.Is(err, ErrNoVision) {
		t.Errorf("a primary that can't download them: %v", err)
	}
	old := &Client{Path: oldPrimary(t, make(chan request, 4))}
	if _, err := old.ReadText(context.Background(), img); !errors.Is(err, ErrNoVision) {
		t.Errorf("an older primary: %v", err)
	}
	if _, _, err := old.FetchVision("u1", true); !errors.Is(err, ErrNoVision) {
		t.Errorf("an older primary's fetch: %v", err)
	}
}

// A child's ask for the download reaches the primary with who it is, and
// never waits behind the photos being read.
func TestAChildAsksForTheVisionModels(t *testing.T) {
	path := socketPath(t)
	var who string
	var want bool
	m := Models{Tagger: func() Tagger { return &fakeTagger{} },
		VisionFetch: func(w string, on bool) EmbedderState {
			who, want = w, on
			return EmbedderState{CanFetch: true, Downloading: true, Percent: 40}
		},
		VisionState: func() EmbedderState { return EmbedderState{CanFetch: true, Downloading: true, Percent: 40} },
	}
	go ServeModels(path, m, 1)
	waitForSocket(path)
	c := &Client{Path: path}
	has, st, err := c.FetchVision("user-7", true)
	if err != nil || has || !st.Downloading || st.Percent != 40 || who != "user-7" || !want {
		t.Errorf("FetchVision = %v %+v %v (asked by %q, %v)", has, st, err, who, want)
	}
	if _, st, err := c.VisionStatus(); err != nil || st.Percent != 40 {
		t.Errorf("VisionStatus = %+v %v", st, err)
	}
}
