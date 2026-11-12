// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"image"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	"github.com/alonsovidales/otc/log"
	petfinder "github.com/alonsovidales/otc/pet_finder"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/google/uuid"
)

// Pets (release 180): the dogs and cats in the photos, each told apart from
// the others as people are by their faces and filed in People as a person
// (owner, 2026-11-12: "they should be faces in the same way as people"):
// named, merged, taken off a photo, added by hand, searched by name and in
// On this day as anyone. While the user has Pets on (settings.pets_enabled,
// on by default) the slow lane looks at each new photo after Smart search,
// and the background pass at what the device held before (BackfillVision):
//
//  1. Whether it may show a pet, for nothing: its Smart search vector
//     against "a photo of a dog", "... of a cat", "... of a pet" (petGate).
//     On the demo library a cosine of 0.06 let 12 of its 13 photos with a
//     dog through and none of the other 102; cPetGate is a little under. A
//     photo without a vector (Smart search off) is looked at anyway.
//  2. Where: pet_finder's D-FINE small, on the photo at up to 4 MP.
//  3. Which pet: DINOv2 small's embedding of the animal, matched against
//     each pet's references as a face is against people's (face_refs.go) -
//     a dog against dogs, a cat against cats - at petSame at least, each
//     pet at most once in a photo (assignPets).
//  4. And then grouped (pet_groups.go): the pets that are one - a pet's
//     photos far apart, filed as several - become one, as a merge in People
//     makes them.
//
// A pet's face is a faces row of its kind (dao.AddPetFace): the whole
// animal's crop and embedding, sealed as faces are; pet_scans keeps the
// photos looked at. The two kinds of embeddings never meet: a person's are
// matched against people's faces (kind 0) alone.
//
// The face model takes many dogs' faces for people's (YuNet: on the demo
// library most photos of its dog made an unnamed "person"), so a dog showed
// twice in People. Once a photo's pets are filed, each person the pet
// detector saw there is given one of its faces - the faces on no pet
// first, then the highest in the person (petsOwn) - and a face on a pet
// that no person was given is that pet's own: taken off its person as Not
// this person does (petsOwnFaces) - the row stays, so the photo isn't
// analysed again. The people are there in most photos of a dog (someone
// holding it, sitting with it, the family around it), so being in a
// person's box alone isn't being theirs. A named person's faces are never
// taken off: who the owner named stays as they named it. The faces are
// all there by then: the slow lane finds a photo's faces before its pets,
// and a Reprocess forgets both.

const (
	// cPetGate: a photo whose vector is at least this close to one of
	// petPrompts' may show a pet.
	cPetGate = 0.05
	// cSamePersonIoU: two of the pet detector's person boxes overlapping at
	// least this much (intersection over union) are one person seen twice,
	// as it sometimes does: on the demo library a woman holding the dog was
	// there twice at 0.87, and four people close together overlapped at
	// 0.42 at most.
	cSamePersonIoU = 0.7
	// cDogSame and cCatSame: the least cosine to one of a pet's references
	// at which a pet just found is that pet, filed one photo at a time
	// (filePets) - seldom another animal: the grouping that follows
	// (pet_groups.go) joins pets, and parts one only where it is twice in
	// a photo. cDogGroup and
	// cCatGroup: the least average cosine between two pets' photos at which
	// they are one (pet_groups.go), and between a photo and a pet's
	// references at which it is like that pet as a whole (kept as a
	// reference once it has cMaxFaceRefsPerPerson, a candidate for its
	// face). Measured on whole-animal crops (D-FINE boxes padded 10%):
	// libraries of 1 to 3 pets with all their photos (11 dogs and 7 cats
	// of Wikimedia Commons, 10 to 25 photos each) among 40 animals seen
	// once (Oxford-IIIT Pet), filed in random order. Release 180's filing
	// alone (0.60, no grouping) left a dog's photos in 5.3 pets, 63% in the
	// largest; filed at 0.60 and grouped at 0.35, in 1.6 pets, 91% in the
	// largest (2% of it another animal's). A cat looks more like any cat
	// to the model: release 180 left a cat's photos in 4.3 pets, 68% in
	// the largest (9% of it other cats); filed at 0.65 and grouped at 0.50,
	// in 3.2 pets, 77% in the largest (9% others').
	cDogSame  = 0.60
	cCatSame  = 0.65
	cDogGroup = 0.35
	cCatGroup = 0.50
)

// petPrompts are what a photo with a pet is like, to Smart search's model.
var petPrompts = []string{"a photo of a dog", "a photo of a cat", "a photo of a pet"} // i18n-ignore: what the image model is asked, in the English it learned; never shown

// petState is Pets' switch and the gate's prompt vectors.
type petState struct {
	loadMu sync.Mutex
	loaded atomic.Bool
	on     atomic.Bool
	// promptsMu guards prompts: petPrompts through Smart search's text
	// model, made once per process (nil until then).
	promptsMu sync.Mutex
	prompts   [][]float32
	// sinceGrouped counts the pets filed since they were last grouped
	// (pet_groups.go); backfilling is whether the background pass is
	// running, which groups them when it is done; groupedOnce whether it
	// grouped them since the device started.
	sinceGrouped atomic.Int64
	backfilling  atomic.Bool
	groupedOnce  atomic.Bool
}

// loadPets reads the switch from the database once, as loadTexts does.
func (mg *Manager) loadPets() {
	if mg == nil || mg.dao == nil {
		return
	}
	s := &mg.pets
	if s.loaded.Load() {
		return
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if s.loaded.Load() {
		return
	}
	on, err := mg.dao.PetsEnabled()
	if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
		log.Error("pets: could not read whether Pets is on, off for now:", err)
		return
	}
	s.on.Store(on)
	s.loaded.Store(true)
}

// PetsEnabled is whether Pets is on for this library.
func (mg *Manager) PetsEnabled() bool {
	if mg == nil {
		return false
	}
	mg.loadPets()
	return mg.pets.on.Load()
}

// SetPetsEnabled turns Pets on or off for this library. On looks at what
// the device holds in the background with ses's key, once the models are
// there (BackfillVision); off finds no more pets and deletes nothing.
func (mg *Manager) SetPetsEnabled(ses *session.Session, on bool) error {
	mg.loadPets()
	s := &mg.pets
	s.loadMu.Lock()
	err := mg.dao.SetPetsEnabled(on)
	if err == nil {
		s.on.Store(on)
		s.loaded.Store(true)
	}
	s.loadMu.Unlock()
	if err != nil {
		return err
	}
	mg.wantVision()
	if on {
		log.Info("pets: on, looking at the photos")
		visionBackfill(mg, ses)
	} else {
		log.Info("pets: off")
	}
	return nil
}

// petPromptVectors is petPrompts through Smart search's text model - made
// once - or nil without it.
func (mg *Manager) petPromptVectors() [][]float32 {
	s := &mg.pets
	s.promptsMu.Lock()
	defer s.promptsMu.Unlock()
	if s.prompts != nil {
		return s.prompts
	}
	e := mg.smartEmbedder()
	if e == nil {
		return nil
	}
	vecs := make([][]float32, 0, len(petPrompts))
	for _, p := range petPrompts {
		ctx, cancel := context.WithTimeout(context.Background(), cEmbedTimeout)
		v, err := e.EmbedText(ctx, p)
		cancel()
		if err != nil {
			log.Debug("pets: could not make the gate's prompts:", err)
			return nil
		}
		vecs = append(vecs, v)
	}
	s.prompts = vecs
	return vecs
}

// petGate says whether a photo whose Smart search vector is vec may show a
// pet - known false when it can't say (no vector, no model).
func (mg *Manager) petGate(vec []float32) (may, known bool) {
	if len(vec) == 0 {
		return false, false
	}
	prompts := mg.petPromptVectors()
	if prompts == nil {
		return false, false
	}
	for _, p := range prompts {
		if len(p) != len(vec) {
			return false, false
		}
		var dot float32
		for i := range p {
			dot += p[i] * vec[i]
		}
		if dot >= cPetGate {
			return true, true
		}
	}
	return false, true
}

// storedVector is hash's Smart search vector as kept, decrypted; nil
// without one.
func (mg *Manager) storedVector(ses *session.Session, hash string, sealed []byte) []float32 {
	if sealed == nil {
		var err error
		if sealed, err = mg.dao.PhotoEmbedding(hash, cEmbedModel); err != nil || sealed == nil {
			return nil
		}
	}
	plain, err := ses.Decrypt(sealed)
	if err != nil {
		return nil
	}
	scale, q, err := unpackVec(plain)
	if err != nil {
		return nil
	}
	vec := make([]float32, len(q))
	for i, v := range q {
		vec[i] = scale * float32(v)
	}
	return vec
}

// petScanned is dao.PetScanned, true when it can't be answered (the photo
// is then left for the next pass).
func (mg *Manager) petScanned(hash string) bool {
	scanned, err := mg.dao.PetScanned(hash, petfinder.Model)
	if err != nil {
		if !errors.Is(err, dao.ErrSchemaBehind) {
			log.Error("pets: could not check whether", hash, "was looked at:", err)
		}
		return true
	}
	return scanned
}

// markPetScanned records that hash was looked at for pets.
func (mg *Manager) markPetScanned(hash string) {
	if err := mg.dao.SetPetScanned(hash, petfinder.Model); err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
		log.Error("pets: could not record that", hash, "was looked at:", err)
	}
}

// processPets looks for pets in a photo the slow lane is analysing, or the
// background pass, from img - the photo itself, decoded upright - while
// Pets is on and its models are here. vec is its Smart search vector when
// the caller has it (nil: the one kept, if any). Looked at once per
// content.
func (mg *Manager) processPets(ses *session.Session, file *pb.File, img image.Image, vec []float32) {
	if ses == nil || img == nil || !strings.HasPrefix(file.Mime, "image") || !mg.PetsEnabled() {
		return
	}
	if mg.visionModel() == nil {
		mg.leftForVision()
		return
	}
	if mg.petScanned(file.Hash) {
		return
	}
	if vec == nil {
		vec = mg.storedVector(ses, file.Hash, nil)
	}
	if may, known := mg.petGate(vec); known && !may {
		mg.markPetScanned(file.Hash)
		return
	}
	started := time.Now()
	found, err := mg.findPets(img)
	if err != nil {
		log.Debug("pets: could not look at", file.Hash, ":", err)
		return
	}
	mg.filePets(ses, file.Hash, found)
	log.Debug("pets:", len(found.Pets), "in", file.Hash, "in", time.Since(started).Round(time.Millisecond))
}

// filePets stores the pets found in hash, each as the pet it is most like
// or a new one, and records that hash was looked at - under faceRefsMu, as
// processFaces stores faces, and only if no other analysis of the same
// content did meanwhile.
func (mg *Manager) filePets(ses *session.Session, hash string, found petfinder.Found) {
	pets := found.Pets
	mg.faceRefsMu.Lock()
	defer mg.faceRefsMu.Unlock()
	if mg.petScanned(hash) {
		return
	}
	if len(pets) > 0 {
		refs, err := mg.loadPetRefsLocked(ses)
		if err != nil {
			log.Error("pets: error listing the pets found before:", err)
			return
		}
		who := assignPets(pets, refs)
		for i, p := range pets {
			kind := int(p.Kind)
			if kind != dao.FaceKindDog && kind != dao.FaceKindCat {
				continue
			}
			kr := refs[kind]
			if kr == nil {
				kr = faceRefs{}
				refs[kind] = kr
			}
			personID := who[i]
			if personID == "" {
				personID = uuid.New().String()
				if err := mg.dao.CreatePerson(personID); err != nil {
					log.Error("pets: error creating a person for a pet just found:", err)
					continue
				}
			}
			faceID := uuid.New().String()
			quality := faceQuality(p.Thumbnail, p.W, p.H)
			embedding := facerecognition.EncodeEmbedding(p.Embedding)
			if err := mg.dao.AddPetFace(faceID, hash, personID, kind, p.X, p.Y, p.W, p.H, ses.Encrypt(embedding), ses.Encrypt(p.Thumbnail), quality); err != nil {
				log.Error("pets: error storing a pet just found:", err)
				continue
			}
			kept := kr.addPet(kind, personID, faceID, p.Embedding)
			if pr := kr[personID]; coverMayChange(pr, kept, quality) {
				mg.chooseCoverLocked(ses, personID, pr)
			}
			mg.pets.sinceGrouped.Add(1)
		}
		mg.takeOffPetsOwnFacesLocked(hash, found)
	}
	mg.markPetScanned(hash)
	// The background pass groups them when it is done; the slow lane does
	// every cPetRegroupEvery pets.
	if !mg.pets.backfilling.Load() && mg.pets.sinceGrouped.Load() >= cPetRegroupEvery {
		mg.regroupPetsLocked(ses)
	}
}

// takeOffPetsOwnFacesLocked takes off their person the faces of hash the
// face model found on its pets (petsOwnFaces), each unnamed person's at
// once, as Not this person does; their references are built again before
// the next match. The caller holds faceRefsMu.
func (mg *Manager) takeOffPetsOwnFacesLocked(hash string, found petfinder.Found) {
	faces, err := mg.dao.PersonFacesIn(hash)
	if err != nil {
		if !errors.Is(err, dao.ErrSchemaBehind) {
			log.Error("pets: could not read the faces of", hash, ":", err)
		}
		return
	}
	took := false
	for _, personID := range petsOwnFaces(faces, found) {
		if _, _, err := mg.dao.RemoveFromPerson(personID, []string{hash}, ""); err != nil {
			log.Error("pets: could not take a pet's face off its person in", hash, ":", err)
			continue
		}
		if mg.faceRefsStale == nil {
			mg.faceRefsStale = map[string]bool{}
		}
		mg.faceRefsStale[personID] = true
		took = true
		log.Debug("pets: took a pet's own face off an unnamed person in", hash)
	}
	if took {
		mg.memories.changed()
	}
}

// petsOwnFaces is the unnamed people whose face in a photo is one of its
// pets' own (petsOwn), of faces - every face the face model found there,
// anyone's: each once, in the order of faces, and only someone whose every
// face there is a pet's.
func petsOwnFaces(faces []dao.PlacedFace, found petfinder.Found) []string {
	boxes := make([]image.Rectangle, len(faces))
	for i, f := range faces {
		boxes[i] = image.Rect(f.Box.X, f.Box.Y, f.Box.X+f.Box.W, f.Box.Y+f.Box.H)
	}
	pets := make([]image.Rectangle, len(found.Pets))
	for i, p := range found.Pets {
		pets[i] = image.Rect(p.X, p.Y, p.X+p.W, p.Y+p.H)
	}
	own := petsOwn(boxes, pets, found.People)
	keep := map[string]bool{}
	for i, f := range faces {
		if !own[i] {
			keep[f.PersonID] = true
		}
	}
	var out []string
	for i, f := range faces {
		if own[i] && f.PersonID != "" && !f.Named && !keep[f.PersonID] {
			keep[f.PersonID] = true // once
			out = append(out, f.PersonID)
		}
	}
	return out
}

// petsOwn is which of a photo's faces (the face model's boxes) are one of
// its pets' own (pets: their boxes). Each person the pet detector saw
// there (people, a person seen twice once) is given one face whose centre
// is in their box: the faces on no pet first, then those on one; the
// highest in the box first, and of those on a pet the least on it - a
// person's face against a dog's is mostly off it. A face whose centre is
// on a pet that no person was given is the pet's own. One that a person
// whose face the face model missed (turned away) was given stays theirs:
// there's no telling.
func petsOwn(faces, pets, people []image.Rectangle) []bool {
	own := make([]bool, len(faces))
	if len(faces) == 0 || len(pets) == 0 {
		return own
	}
	people = distinctPeople(people)
	type claim struct {
		face, person int
		onPet        bool
		cost         float64
	}
	var claims []claim
	onPet := make([]bool, len(faces))
	for i, f := range faces {
		c := image.Pt((f.Min.X+f.Max.X)/2, (f.Min.Y+f.Max.Y)/2)
		on := 0.0
		for _, p := range pets {
			if c.In(p) {
				onPet[i] = true
			}
			on = max(on, coverage(f, p))
		}
		for j, r := range people {
			if !c.In(r) {
				continue
			}
			cost := float64(c.Y-r.Min.Y) / float64(r.Dy())
			if onPet[i] {
				cost += on
			}
			claims = append(claims, claim{i, j, onPet[i], cost})
		}
	}
	sort.SliceStable(claims, func(a, b int) bool {
		if claims[a].onPet != claims[b].onPet {
			return !claims[a].onPet
		}
		return claims[a].cost < claims[b].cost
	})
	given := make([]bool, len(faces))
	taken := make([]bool, len(people))
	for _, c := range claims {
		if !given[c.face] && !taken[c.person] {
			given[c.face], taken[c.person] = true, true
		}
	}
	for i := range faces {
		own[i] = onPet[i] && !given[i]
	}
	return own
}

// distinctPeople is people with a person seen twice (two boxes at
// cSamePersonIoU or more) once, the first box kept.
func distinctPeople(people []image.Rectangle) []image.Rectangle {
	var out []image.Rectangle
	for _, r := range people {
		if r.Empty() {
			continue
		}
		twice := false
		for _, o := range out {
			if overlap(r, o) >= cSamePersonIoU {
				twice = true
				break
			}
		}
		if !twice {
			out = append(out, r)
		}
	}
	return out
}

// overlap is a and b's intersection over their union.
func overlap(a, b image.Rectangle) float64 {
	in := boxArea(a.Intersect(b))
	if in == 0 {
		return 0
	}
	return in / (boxArea(a) + boxArea(b) - in)
}

// coverage is how much of f is in p.
func coverage(f, p image.Rectangle) float64 {
	if a := boxArea(f); a > 0 {
		return boxArea(f.Intersect(p)) / a
	}
	return 0
}

func boxArea(r image.Rectangle) float64 { return float64(r.Dx()) * float64(r.Dy()) }

// petSame is the least cosine to one of a pet's references at which a pet
// of kind just found is them (cDogSame, cCatSame).
func petSame(kind int) float32 {
	if kind == dao.FaceKindCat {
		return cCatSame
	}
	return cDogSame
}

// petGroupAt is the least average cosine at which two pets of kind are one
// (cDogGroup, cCatGroup).
func petGroupAt(kind int) float32 {
	if kind == dao.FaceKindCat {
		return cCatGroup
	}
	return cDogGroup
}

// addPet offers a pet's face to its references in kr, a kind's set.
func (kr faceRefs) addPet(kind int, personID, faceID string, emb []float32) bool {
	return kr.addWith(personID, faceID, emb, petSame(kind), petGroupAt(kind))
}

// assignPets is who each of a photo's pets is, of the pets already found
// (refs, by kind): the pet of its kind it is closest to - its closest
// reference at petSame or more - the closest pairs first, and each pet
// once: one animal is never twice in a photo, so two dogs alike side by
// side (in a kennel, on a sofa) are two pets. "" for a pet left: a new one.
func assignPets(pets []petfinder.Pet, refs map[int]faceRefs) []string {
	type pair struct {
		pet    int
		person string
		score  float32
	}
	var pairs []pair
	for i, p := range pets {
		for personID, pr := range refs[int(p.Kind)] {
			if personID == "" {
				continue // no one's faces are never references
			}
			best := float32(-2)
			for _, r := range pr.refs {
				if s := facerecognition.CosineSimilarity(p.Embedding, r.emb); s > best {
					best = s
				}
			}
			if pr.isSame(best) {
				pairs = append(pairs, pair{i, personID, best})
			}
		}
	}
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].score != pairs[b].score {
			return pairs[a].score > pairs[b].score
		}
		if pairs[a].pet != pairs[b].pet {
			return pairs[a].pet < pairs[b].pet
		}
		return pairs[a].person < pairs[b].person
	})
	out := make([]string, len(pets))
	taken := map[string]bool{}
	for _, pr := range pairs {
		if out[pr.pet] == "" && !taken[pr.person] {
			out[pr.pet], taken[pr.person] = pr.person, true
		}
	}
	return out
}

// loadPetRefsLocked is the pets' matching set by kind, as loadFaceRefsLocked
// is the people's: built from every stored pet face at first use, and
// again after anything took faces away (forgetFacesLocked,
// InvalidateFaceRefs). The caller holds faceRefsMu.
func (mg *Manager) loadPetRefsLocked(ses *session.Session) (map[int]faceRefs, error) {
	if mg.petRefs != nil {
		return mg.petRefs, nil
	}
	stored, err := mg.dao.ListPetEmbeddings()
	if err != nil {
		return nil, err
	}
	refs := map[int]faceRefs{}
	for _, e := range stored {
		plain, err := ses.Decrypt(e.Embedding)
		if err != nil {
			log.Error("pets: error decrypting a stored pet's embedding, skipping it:", err)
			continue
		}
		kr := refs[e.Kind]
		if kr == nil {
			kr = faceRefs{}
			refs[e.Kind] = kr
		}
		kr.addPet(e.Kind, e.PersonID, e.ID, facerecognition.DecodeEmbedding(plain))
	}
	mg.petRefs = refs
	return refs, nil
}

// forgetPetScans forgets that content nothing uses any more, or kept out of
// Images, was looked at for pets.
func (mg *Manager) forgetPetScans(hashes ...string) {
	if len(hashes) == 0 {
		return
	}
	if err := mg.dao.DelPetScans(hashes...); err != nil {
		log.Error("could not forget that", len(hashes), "content(s) were looked at for pets:", err)
	}
}
