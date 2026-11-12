// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"image"
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
//     a dog against dogs, a cat against cats - at cPetSame at least.
//
// A pet's face is a faces row of its kind (dao.AddPetFace): the whole
// animal's crop and embedding, sealed as faces are; pet_scans keeps the
// photos looked at. The two kinds of embeddings never meet: a person's are
// matched against people's faces (kind 0) alone.
//
// The face model takes many dogs' faces for people's (YuNet: on the demo
// library most photos of its dog made an unnamed "person"), so a dog showed
// twice in People. A person's face found inside a pet's box and inside no
// person the pet detector saw is that pet's own: once the pet is filed it is
// taken off its person as Not this person does (petsOwnFaces) - the row
// stays, so the photo isn't analysed again. A named person's faces are never
// touched: who the owner named stays as they named it.

const (
	// cPetGate: a photo whose vector is at least this close to one of
	// petPrompts' may show a pet.
	cPetGate = 0.05
	// cPetSame: the least cosine at which two pets' embeddings are the same
	// pet. Measured on whole-animal crops (D-FINE boxes padded 10%):
	// Biscuit's photos against each other kept 73% of the pairs, and 12% of
	// the pairs of different dogs - most of them golden retrievers and
	// labradors, as he is - were over it. Matching is to the nearest of a
	// pet's references, so most of a pet's photos join them; a lookalike
	// that joins is taken off with Not this person, as with people.
	cPetSame = 0.60
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
	if ses == nil || img == nil || !strings.HasPrefix(file.Mime, "image") || !mg.PetsEnabled() || mg.visionModel() == nil {
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
		for _, p := range pets {
			kind := int(p.Kind)
			if kind != dao.FaceKindDog && kind != dao.FaceKindCat {
				continue
			}
			kr := refs[kind]
			if kr == nil {
				kr = faceRefs{}
				refs[kind] = kr
			}
			personID := matchIn(p.Embedding, kr, isSamePet)
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
			kept := kr.addSame(personID, faceID, p.Embedding, cPetSame)
			if pr := kr[personID]; coverMayChange(pr, kept, quality) {
				mg.chooseCoverLocked(ses, personID, pr)
			}
		}
		mg.takeOffPetsOwnFacesLocked(hash, found)
	}
	mg.markPetScanned(hash)
}

// takeOffPetsOwnFacesLocked takes off their person the faces of hash the
// face model found on its pets (petsOwnFaces), each unnamed person's at
// once, as Not this person does; their references are built again before
// the next match. The caller holds faceRefsMu.
func (mg *Manager) takeOffPetsOwnFacesLocked(hash string, found petfinder.Found) {
	faces, err := mg.dao.UnnamedFacesIn(hash)
	if err != nil || len(faces) == 0 {
		if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
			log.Error("pets: could not read the faces of", hash, ":", err)
		}
		return
	}
	for _, personID := range petsOwnFaces(faces, found) {
		if _, _, err := mg.dao.RemoveFromPerson(personID, []string{hash}, ""); err != nil {
			log.Error("pets: could not take a pet's face off its person in", hash, ":", err)
			continue
		}
		if mg.faceRefsStale == nil {
			mg.faceRefsStale = map[string]bool{}
		}
		mg.faceRefsStale[personID] = true
		log.Debug("pets: took a pet's own face off an unnamed person in", hash)
	}
	mg.memories.changed()
}

// petsOwnFaces is the people whose face in a photo is one of its pets':
// the face's centre inside a pet's box and in no person's box the pet
// detector saw. Each person once, in the order of faces.
func petsOwnFaces(faces []dao.PlacedFace, found petfinder.Found) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range faces {
		c := image.Pt(f.Box.X+f.Box.W/2, f.Box.Y+f.Box.H/2)
		onPet := false
		for _, p := range found.Pets {
			if c.In(image.Rect(p.X, p.Y, p.X+p.W, p.Y+p.H)) {
				onPet = true
				break
			}
		}
		if !onPet {
			continue
		}
		onPerson := false
		for _, r := range found.People {
			if c.In(r) {
				onPerson = true
				break
			}
		}
		if !onPerson && !seen[f.PersonID] {
			seen[f.PersonID] = true
			out = append(out, f.PersonID)
		}
	}
	return out
}

// isSamePet is cPetSame's test.
func isSamePet(score float32) bool { return score >= cPetSame }

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
		kr.addSame(e.PersonID, e.ID, facerecognition.DecodeEmbedding(plain), cPetSame)
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
