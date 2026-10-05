// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"image"

	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/google/uuid"
)

// processFaces runs face detection+recognition on a newly uploaded photo
// (issue #52), called from UploadFile's background goroutine right after
// its thumbnail is written. Gated by the face_recognition_enabled setting
// checked *right now*, not later - enabling the feature only ever affects
// photos uploaded from that point on. A photo uploaded while this was off
// is never revisited just because the setting gets turned on afterward:
// there's no backfill job, and deliberately so - the owner always knows
// exactly which photos were scanned purely from when they were uploaded
// relative to the toggle, never a surprise retroactive sweep of a library
// that might be years old. See db.sql's `faces` table doc comment for the
// storage side of the same reasoning.
//
// One more model pass on top of the RAM++ tagger's own (see the tagging
// block just above this call in UploadFile) - not free, which is exactly
// why this stays off by default.
//
// Matching runs against the in-memory reference set (issue #173, see
// face_refs.go and loadFaceRefsLocked), not every stored face: this used
// to read and decrypt the whole faces table for every photo and recompute
// a person's medoid over all of their faces after every face, which made
// a full-library Reprocess take hours.
func (mg *Manager) processFaces(ses *session.Session, file *pb.File, img image.Image) {
	if mg.faceRecognizer == nil {
		return // [faces] not configured on this device - see Init
	}
	enabled, err := mg.dao.GetFaceRecognitionEnabled()
	if err != nil {
		log.Error("error checking face_recognition_enabled, skipping face detection:", err)
		return
	}
	if !enabled {
		return
	}
	// Faces are keyed by content, like tags and thumbnails, and found once:
	// a second pass on the same content (a backfill, a resumed reprocess,
	// two uploads of it at once) stored a second set of rows for the same
	// faces. A Reprocess wipes them first, so it still detects everything.
	if mg.hasFaces(file.Hash) {
		return
	}

	detections, err := mg.faceRecognizer.DetectFaces(img)
	if err != nil {
		log.Error("error detecting faces:", err)
		return
	}
	if len(detections) == 0 {
		return
	}

	// Detection above runs unlocked (it's the expensive model pass, and
	// independent per photo); matching and storing is serialized on the
	// shared reference set.
	mg.faceRefsMu.Lock()
	defer mg.faceRefsMu.Unlock()
	// Again under the lock: another analysis of the same content may have
	// stored its faces while this one was detecting.
	if mg.hasFaces(file.Hash) {
		return
	}

	refs, err := mg.loadFaceRefsLocked(ses)
	if err != nil {
		log.Error("error listing existing face embeddings:", err)
		return
	}

	for _, det := range detections {
		personID := matchOrNewPerson(det.Embedding, refs)
		if personID == "" {
			personID = uuid.New().String()
			if err := mg.dao.CreatePerson(personID); err != nil {
				log.Error("error creating person for a newly detected face:", err)
				continue
			}
		}

		encoded := facerecognition.EncodeEmbedding(det.Embedding)
		faceID := uuid.New().String()
		if err := mg.dao.AddFace(faceID, file.Hash, personID, det.X, det.Y, det.W, det.H, ses.Encrypt(encoded), ses.Encrypt(det.Thumbnail)); err != nil {
			log.Error("error storing a detected face:", err)
			continue
		}

		// So a second face in this same photo (or a later photo) can also
		// match this person through this face - subject to the reference
		// rule (issue #173, see personFaceRefs.add). The face row above is
		// stored either way; the cover face only needs recomputing when
		// the references actually changed.
		if refs.add(personID, faceID, det.Embedding) {
			updatePersonCoverFace(mg.dao, personID, refs[personID])
		}
	}
}

// hasFaces is dao.HashHasFaces, false when it can't be answered (the
// faces are then detected, as they always were).
func (mg *Manager) hasFaces(hash string) bool {
	has, err := mg.dao.HashHasFaces(hash)
	if err != nil {
		log.Error("could not check for the faces already found in", hash, ":", err)
		return false
	}
	return has
}

// loadFaceRefsLocked returns the cached reference set, building it on
// first use (issue #173): every stored embedding is read and decrypted
// once per process (or once after InvalidateFaceRefs) instead of once per
// photo, and each person's references are picked with the same rule
// processFaces applies incrementally (personFaceRefs.add), offering faces
// in the database's row order. A person a delete took a reference from
// (faceRefsStale) is rebuilt the same way from their own faces only. The
// caller must hold faceRefsMu.
func (mg *Manager) loadFaceRefsLocked(ses *session.Session) (faceRefs, error) {
	if mg.faceRefs != nil {
		for personID := range mg.faceRefsStale {
			faces, err := mg.dao.ListPersonFaceEmbeddings(personID)
			if err != nil {
				log.Error("error listing a person's face embeddings, reading everyone's:", err)
				mg.faceRefs = nil
				break
			}
			// Built afresh: with no face left (the person went too, or
			// kept only a name) they stay out, as a full build leaves them.
			delete(mg.faceRefs, personID)
			mg.faceRefs.addStored(ses, faces)
		}
		mg.faceRefsStale = nil
	}
	if mg.faceRefs != nil {
		return mg.faceRefs, nil
	}
	existing, err := mg.dao.ListFaceEmbeddings()
	if err != nil {
		return nil, err
	}
	refs := faceRefs{}
	refs.addStored(ses, existing)
	mg.faceRefs, mg.faceRefsStale = refs, nil
	return refs, nil
}

// addStored offers stored faces to their people's references, in order.
func (fr faceRefs) addStored(ses *session.Session, faces []dao.FaceEmbedding) {
	for _, e := range faces {
		// Everything under faces.* is derived straight from the owner's
		// own photos - a face crop is as much "file content" as the photo
		// it was cut from, and an embedding is a biometric fingerprint of
		// it - so both get the same at-rest encryption as thumbnails/
		// originals elsewhere in files_manager (see GetThumbnail's
		// session.Decrypt). Only the decrypted references stay in this
		// process's memory, never on disk.
		plain, err := ses.Decrypt(e.Embedding)
		if err != nil {
			log.Error("error decrypting a stored face embedding, skipping it:", err)
			continue
		}
		fr.add(e.PersonID, e.ID, facerecognition.DecodeEmbedding(plain))
	}
}

// InvalidateFaceRefs drops the cached matching set (issue #173) so the
// next processFaces rebuilds it from the database. Called after anything
// outside processFaces changes faces or people - deleting or merging
// people, wiping faces for a Reprocess - since the cache would otherwise
// keep matching against faces or person ids that no longer exist.
// Rebuilding lazily is simpler and safer than patching the cache in place,
// and those operations are rare. Safe on a nil Manager, for callers
// wired up without files_manager.
func (mg *Manager) InvalidateFaceRefs() {
	if mg == nil {
		return
	}
	mg.faceRefsMu.Lock()
	mg.faceRefs = nil
	mg.faceRefsMu.Unlock()
}

// dropFacesOfHash removes the faces found in content nothing uses any
// more (its last file or version is gone), with what goes with them - see
// dao.DelFacesByHash - and takes them out of the matching set. Under
// faceRefsMu, which processFaces holds from matching to storing, so it
// can't add a face to a person deleted here, or have one of its faces
// deleted mid-way. Callers hold the hash's lock (the order is always the
// hash's lock, then faceRefsMu: processFaces takes no hash lock). A
// failure is only logged: the delete that got here has already happened.
func (mg *Manager) dropFacesOfHash(hash string) {
	mg.faceRefsMu.Lock()
	defer mg.faceRefsMu.Unlock()
	gone, err := mg.dao.DelFacesByHash(hash)
	if err != nil {
		// Whether the faces went is unknown: build the set again from
		// what is stored.
		mg.faceRefs = nil
		log.Error("could not remove the faces of deleted content", hash, ":", err)
		return
	}
	if len(gone) > 0 {
		mg.forgetFacesLocked(gone)
		log.Debug("removed", len(gone), "face(s) of deleted content", hash)
	}
}

// forgetFacesLocked takes deleted faces out of the matching set. Dropping
// the whole set instead made the next photo with faces read and decrypt
// every stored embedding again - issue #173's per-photo cost, once per
// deleted photo while a folder went and photos were being analysed, all
// under faceRefsMu, which removeBlobIfUnused waits for holding a blob
// lock. A person who lost one of their references is marked stale and
// rebuilt from their own faces before the next match, as a full build
// would have done: that refills their set from the faces they have left,
// and leaves them out when there are none. A face that wasn't a reference
// changes nothing. Faces are added only under faceRefsMu, held here, so
// gone is every face that went. The caller holds faceRefsMu.
func (mg *Manager) forgetFacesLocked(gone []dao.DeletedFace) {
	if mg.faceRefs == nil {
		return // built when next needed, from what is left
	}
	for _, f := range gone {
		p := mg.faceRefs[f.PersonID]
		if p == nil {
			continue
		}
		for i, r := range p.refs {
			if r.id == f.ID {
				p.remove(i)
				if mg.faceRefsStale == nil {
					mg.faceRefsStale = map[string]bool{}
				}
				mg.faceRefsStale[f.PersonID] = true
				break
			}
		}
	}
}

// sweepOrphanFaces removes, once per start, the faces of content deleted
// before they went with it (or whose removal failed), each under its
// hash's lock and only if nothing uses the content now.
func (mg *Manager) sweepOrphanFaces() {
	hashes, err := mg.dao.OrphanFaceHashes()
	if err != nil {
		log.Error("could not look for the faces of deleted content:", err)
		return
	}
	for _, h := range hashes {
		unlock := lockBlob(h)
		if referenced, err := mg.dao.HashReferenced(h); err == nil && !referenced {
			mg.dropFacesOfHash(h)
		}
		unlock()
	}
	if len(hashes) > 0 {
		log.Info("removed the faces of", len(hashes), "deleted photo(s)")
	}
}

// updatePersonCoverFace recomputes and persists a person's medoid cover
// face (face_recognition.MedoidFaceID's own doc comment has the full
// reasoning) over that person's references only - at most
// cMaxFaceRefsPerPerson faces, so <=400 comparisons instead of O(k^2) over
// every face they have (issue #173). The references are kept diverse and
// free of outliers, so their medoid is still a representative face, and
// its cohesion still flags a chained-together non-person. Run whenever a
// person's references change, not just for new people, since the medoid
// can move as more (possibly better) photos of them come in.
func updatePersonCoverFace(d *dao.Dao, personID string, p *personFaceRefs) {
	if p == nil {
		return
	}
	medoidID, cohesion := facerecognition.MedoidAndCohesion(p.identified())
	if medoidID == "" {
		return
	}
	if err := d.SetPersonCoverFace(personID, medoidID, cohesion); err != nil {
		log.Error("error updating person cover face:", err)
	}
}

// matchOrNewPerson returns the person id of whichever reference face is
// the closest match above face_recognition's same-person threshold, or ""
// if none clears it - the caller creates a new person in that case. Only
// each person's references are compared (issue #173), not every face ever
// stored. Pure logic, deliberately separate from processFaces' DB/model
// plumbing so it's directly unit-testable without a real database or the
// actual models.
func matchOrNewPerson(embedding []float32, refs faceRefs) string {
	bestPersonID := ""
	var bestScore float32
	for personID, p := range refs {
		for _, r := range p.refs {
			score := facerecognition.CosineSimilarity(embedding, r.emb)
			if score > bestScore {
				bestScore = score
				bestPersonID = personID
			}
		}
	}
	if bestPersonID != "" && facerecognition.IsSamePersonScore(bestScore) {
		return bestPersonID
	}
	return ""
}
