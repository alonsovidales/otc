// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/alonsovidales/otc/dao"
	petfinder "github.com/alonsovidales/otc/pet_finder"
	"github.com/alonsovidales/otc/session"
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

func (f *fakeFinder) FindAll(ctx context.Context, img image.Image) (petfinder.Found, error) {
	return petfinder.Found{Pets: []petfinder.Pet{{Kind: petfinder.Dog, W: 10, H: 10}}, People: []image.Rectangle{image.Rect(2, 4, 6, 10)}}, nil
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
	found, err := mg.findPets(big)
	pets := found.Pets
	if err != nil || len(pets) != 1 {
		t.Fatalf("findPets = %v, %v", found, err)
	}
	if pets[0].W != 20 || pets[0].H != 20 {
		t.Errorf("box %dx%d, want 20x20 in the photo's pixels", pets[0].W, pets[0].H)
	}
	if len(found.People) != 1 || found.People[0] != image.Rect(4, 8, 12, 20) {
		t.Errorf("the people seen are at %v, want (4,8)-(12,20) in the photo's pixels", found.People)
	}
	if _, err := (&Manager{}).findPets(big); !errors.Is(err, errVisionUnavailable) {
		t.Errorf("without models: %v", err)
	}
}

// A face on a pet - the face model's on a dog's face - is the pet's own
// unless a person the pet detector saw there is given it: each person one
// face, the faces on no pet first, then the highest in them. An unnamed
// person goes once, and only when all their faces there are a pet's.
func TestPetsOwnFaces(t *testing.T) {
	face := func(id, person string, named bool, x0, y0, x1, y1 int) dao.PlacedFace {
		return dao.PlacedFace{ID: id, PersonID: person, Named: named, Box: dao.FaceBox{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}}
	}
	dog := func(x0, y0, x1, y1 int) []petfinder.Pet {
		return []petfinder.Pet{{Kind: petfinder.Dog, X: x0, Y: y0, W: x1 - x0, H: y1 - y0}}
	}
	for _, c := range []struct {
		what  string
		faces []dao.PlacedFace
		found petfinder.Found
		want  []string
	}{
		// Three of the demo library's photos, as the models saw them.
		{
			"held by someone the detector saw twice",
			[]dao.PlacedFace{face("f1", "dogface", false, 1104, 980, 1489, 1471), face("f2", "mum", true, 1499, 357, 1795, 717)},
			petfinder.Found{Pets: dog(328, 717, 1594, 1412), People: []image.Rectangle{image.Rect(417, 205, 1982, 1500), image.Rect(588, 208, 1980, 1529)}},
			[]string{"dogface"},
		},
		{
			"beside the face of the one holding it",
			[]dao.PlacedFace{face("f1", "kid", false, 1037, 664, 1216, 895), face("f2", "dogface", false, 1222, 670, 1414, 909)},
			petfinder.Found{Pets: dog(1182, 657, 2084, 1564), People: []image.Rectangle{image.Rect(769, 597, 1407, 1612)}},
			[]string{"dogface"},
		},
		{
			"four people and the dog",
			[]dao.PlacedFace{
				face("a", "p1", false, 855, 296, 1060, 580), face("b", "p2", false, 890, 993, 1089, 1235), face("c", "p3", false, 1365, 708, 1556, 948),
				face("d", "p4", false, 1440, 193, 1655, 518), face("e", "dogface", false, 1887, 967, 2114, 1276)},
			petfinder.Found{Pets: dog(1774, 978, 2268, 1780), People: []image.Rectangle{
				image.Rect(499, 207, 1226, 1777), image.Rect(1146, 553, 1851, 1781), image.Rect(657, 891, 1298, 1778), image.Rect(1179, 73, 2038, 1239)}},
			[]string{"dogface"},
		},
		{
			"a kid cheek to cheek with the dog, its face a little higher: the kid keeps theirs",
			[]dao.PlacedFace{face("f1", "kid", false, 420, 120, 480, 180), face("f2", "dogface", false, 300, 100, 360, 160)},
			petfinder.Found{Pets: dog(250, 100, 460, 400), People: []image.Rectangle{image.Rect(300, 80, 700, 600)}},
			[]string{"dogface"},
		},
		{
			"held by someone turned away: no telling, kept",
			[]dao.PlacedFace{face("f1", "dogface", false, 150, 120, 210, 180)},
			petfinder.Found{Pets: dog(100, 100, 500, 400), People: []image.Rectangle{image.Rect(80, 50, 600, 700)}},
			nil,
		},
		{
			"named by the owner: kept",
			[]dao.PlacedFace{face("f1", "biscuit", true, 150, 120, 210, 180)},
			petfinder.Found{Pets: dog(100, 100, 500, 400)},
			nil,
		},
		{
			"someone with another face there, on no pet: kept",
			[]dao.PlacedFace{face("f1", "mixed", false, 150, 120, 210, 180), face("f2", "mixed", false, 800, 100, 850, 150)},
			petfinder.Found{Pets: dog(100, 100, 500, 400)},
			nil,
		},
		{
			"each person once, and no one's face is no one to take off",
			[]dao.PlacedFace{face("f1", "dogface", false, 150, 120, 210, 180), face("f2", "dogface", false, 200, 150, 240, 190), face("f3", "", false, 300, 200, 340, 240)},
			petfinder.Found{Pets: dog(100, 100, 500, 400)},
			[]string{"dogface"},
		},
		{
			"no pet",
			[]dao.PlacedFace{face("f1", "someone", false, 150, 120, 210, 180)},
			petfinder.Found{People: []image.Rectangle{image.Rect(80, 50, 600, 700)}},
			nil,
		},
	} {
		if got := petsOwnFaces(c.faces, c.found); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: petsOwnFaces = %v, want %v", c.what, got, c.want)
		}
	}
}

// Photos the slow lane analysed before the models came are read once they
// arrive, with the session held for them - once.
func TestVisionLeftForTheModels(t *testing.T) {
	var started []*session.Session
	old := visionBackfill
	visionBackfill = func(mg *Manager, ses *session.Session) { started = append(started, ses) }
	defer func() { visionBackfill = old }()
	mg := &Manager{}
	ses := faceTestSession(t)
	mg.visionArrived() // nothing held, nothing left
	mg.holdForVision(ses)
	mg.visionArrived() // nothing left
	if len(started) != 0 {
		t.Fatalf("started with nothing left: %d", len(started))
	}
	mg.leftForVision()
	mg.visionArrived()
	if len(started) != 1 || started[0] != ses {
		t.Fatalf("started %d time(s), want once with the session held", len(started))
	}
	mg.visionArrived()
	if len(started) != 1 {
		t.Errorf("started again with nothing left: %d", len(started))
	}
}
