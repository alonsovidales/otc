// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"image"
	"sync"
	"testing"
	"time"

	petfinder "github.com/alonsovidales/otc/pet_finder"
	textreader "github.com/alonsovidales/otc/text_reader"
)

// The gate says a photo may show a pet when its vector is close enough to
// one of the prompts, and can't say without a vector or the prompts.
func TestPetGate(t *testing.T) {
	mg := &Manager{}
	mg.pets.prompts = [][]float32{{1, 0, 0}, {0, 1, 0}}
	for _, c := range []struct {
		vec        []float32
		may, known bool
		what       string
	}{
		{[]float32{0.2, 0, 0.98}, true, true, "like a dog"},
		{[]float32{0, 0.051, 0.99}, true, true, "a cat, just"},
		{[]float32{0.04, 0.03, 0.99}, false, true, "no pet"},
		{nil, false, false, "no vector"},
		{[]float32{1, 0}, false, false, "another model's vector"},
	} {
		may, known := mg.petGate(c.vec)
		if may != c.may || known != c.known {
			t.Errorf("%s: petGate = %v %v, want %v %v", c.what, may, known, c.may, c.known)
		}
	}
	// No prompts and no model to make them: it can't say.
	if _, known := (&Manager{}).petGate([]float32{1, 0, 0}); known {
		t.Error("the gate answered without its prompts")
	}
}

// A pet joins the pet it is closest to at cPetSame or more, of its own
// kind's set; under it, it is a new pet. A person's faces never take
// part.
func TestPetMatching(t *testing.T) {
	dogs := faceRefs{}
	dogs.addSame("biscuit", "f1", normalised([]float32{1, 0.1, 0}), cPetSame)
	dogs.addSame("rex", "f2", normalised([]float32{0, 1, 0.2}), cPetSame)
	if got := matchIn(normalised([]float32{1, 0.3, 0.1}), dogs, isSamePet); got != "biscuit" {
		t.Errorf("a photo of Biscuit matched %q", got)
	}
	// Cosine 0.5 to Biscuit: a person's face would match at that, a pet
	// doesn't.
	if got := matchIn(normalised([]float32{0.5, 0, 0.866}), dogs, isSamePet); got != "" {
		t.Errorf("a stranger matched %q", got)
	}
	if p := dogs["biscuit"]; p.same != cPetSame || !p.isSame(0.61) || p.isSame(0.59) {
		t.Errorf("a pet's references don't hold its threshold: %v", p.same)
	}
	people := faceRefs{}
	people.add("alice", "f3", normalised([]float32{1, 0, 0}))
	if p := people["alice"]; p.same != 0 || !p.isSame(0.4) {
		t.Error("a person's references lost face recognition's threshold")
	}
}

// fakeReader and fakeFinder count their loads, uses and closes.
type fakeReader struct{ closed *int }

func (f *fakeReader) Read(ctx context.Context, img image.Image) ([]textreader.Line, error) {
	return []textreader.Line{{Text: "STOP"}}, nil
}
func (f *fakeReader) Close() { *f.closed++ }

type fakeFinder struct{ closed *int }

func (f *fakeFinder) Find(ctx context.Context, img image.Image) ([]petfinder.Pet, error) {
	return []petfinder.Pet{{Kind: petfinder.Dog, W: 10, H: 10}}, nil
}
func (f *fakeFinder) Close() { *f.closed++ }

// Each model loads when first needed, once, and goes after idle unused -
// never while a call is in it.
func TestVisionModelsLoadAndRelease(t *testing.T) {
	now := time.Unix(1000, 0)
	loads, closes := 0, 0
	m := &visionModels{
		run:  &sync.Mutex{},
		idle: time.Minute,
		now:  func() time.Time { return now },
		loadReader: func() (visionReader, error) {
			loads++
			return &fakeReader{closed: &closes}, nil
		},
		loadFinder: func() (visionFinder, error) { return nil, errors.New("no file") },
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for range 2 {
		if lines, err := m.ReadText(context.Background(), img); err != nil || len(lines) != 1 {
			t.Fatalf("ReadText = %v, %v", lines, err)
		}
	}
	if loads != 1 {
		t.Errorf("the reader loaded %d times", loads)
	}
	if _, err := m.FindPets(context.Background(), img); err == nil {
		t.Error("a finder that can't load answered")
	}
	if r := m.releaseIdle(); len(r) != 0 {
		t.Errorf("released %v right after a use", r)
	}
	now = now.Add(2 * time.Minute)
	if r := m.releaseIdle(); len(r) != 1 || r[0] != "text" || closes != 1 {
		t.Errorf("released %v (%d closes), want the reader", r, closes)
	}
	if _, err := m.ReadText(context.Background(), img); err != nil || loads != 2 {
		t.Errorf("after the release: %v, %d loads", err, loads)
	}
}

// The primary downloads while it or a user's instance wants the models,
// and a user's lease runs out cSmartLease after its last ask.
func TestLocalVisionLeases(t *testing.T) {
	now := time.Unix(5000, 0)
	v := &localVision{leases: map[string]time.Time{}, now: func() time.Time { return now }}
	if v.wanted() {
		t.Fatal("wanted by no one")
	}
	v.wantFor("user1", true)
	if !v.wanted() {
		t.Error("a user's ask isn't a want")
	}
	now = now.Add(cSmartLease + time.Second)
	if v.wanted() {
		t.Error("a lease outlived cSmartLease")
	}
	v.wantFor("", true)
	if !v.wanted() {
		t.Error("the primary's own switch isn't a want")
	}
	v.wantFor("", false)
	if v.wanted() {
		t.Error("still wanted once the primary turned it off")
	}
	if st := v.status(); !st.unavailable {
		t.Errorf("no download directory: status %+v, want unavailable", st)
	}
}

// What a smaller copy of the photo found is in the photo's own pixels.
func TestFindPetsScalesBack(t *testing.T) {
	closes := 0
	mg := &Manager{}
	mg.setVision(&visionModels{run: &sync.Mutex{}, idle: time.Hour, now: time.Now,
		loadFinder: func() (visionFinder, error) { return &fakeFinder{closed: &closes}, nil }})
	big := image.NewNRGBA(image.Rect(0, 0, 4000, 3000)) // 12 MP: halved to 3 MP
	pets, err := mg.findPets(big)
	if err != nil || len(pets) != 1 {
		t.Fatalf("findPets = %v, %v", pets, err)
	}
	if pets[0].W != 20 || pets[0].H != 20 {
		t.Errorf("box %dx%d, want 20x20 in the photo's pixels", pets[0].W, pets[0].H)
	}
	if _, err := (&Manager{}).findPets(big); !errors.Is(err, errVisionUnavailable) {
		t.Errorf("without models: %v", err)
	}
}
