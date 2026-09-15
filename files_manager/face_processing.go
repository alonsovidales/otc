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

	detections, err := mg.faceRecognizer.DetectFaces(img)
	if err != nil {
		log.Error("error detecting faces:", err)
		return
	}
	if len(detections) == 0 {
		return
	}

	existing, err := mg.dao.ListFaceEmbeddings()
	if err != nil {
		log.Error("error listing existing face embeddings:", err)
		return
	}
	// Everything under faces.* is derived straight from the owner's own
	// photos - a face crop is as much "file content" as the photo it was
	// cut from, and an embedding is a biometric fingerprint of it - so both
	// get the same at-rest encryption as thumbnails/originals elsewhere in
	// files_manager (see GetThumbnail's session.Decrypt). Stored rows are
	// decrypted right back here before matchOrNewPerson ever sees them.
	for i := range existing {
		plain, err := ses.Decrypt(existing[i].Embedding)
		if err != nil {
			log.Error("error decrypting a stored face embedding, skipping it:", err)
			continue
		}
		existing[i].Embedding = plain
	}

	for _, det := range detections {
		personID := matchOrNewPerson(det.Embedding, existing)
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

		// So a second face in this same photo (or a later one, further
		// down this same detections loop) can also match this brand new
		// person, not only faces that already existed before this upload.
		// Kept as the plaintext embedding (not the encrypted form just
		// written to AddFace) since this slice only ever feeds
		// matchOrNewPerson, right above.
		existing = append(existing, dao.FaceEmbedding{ID: faceID, PersonID: personID, Embedding: encoded})

		updatePersonCoverFace(mg.dao, personID, existing)
	}
}

// updatePersonCoverFace recomputes and persists personID's medoid cover
// face (face_recognition.MedoidFaceID's own doc comment has the full
// reasoning) using whatever's already in existing - the same in-memory,
// already-decrypted set processFaces just built for matching, filtered
// down to this one person, rather than a fresh DB round trip. Run after
// every single face added to a person, not just new people, since an
// existing person's medoid can change as more (possibly better) photos of
// them come in.
func updatePersonCoverFace(d *dao.Dao, personID string, existing []dao.FaceEmbedding) {
	var faces []facerecognition.IdentifiedEmbedding
	for _, e := range existing {
		if e.PersonID != personID {
			continue
		}
		faces = append(faces, facerecognition.IdentifiedEmbedding{
			ID:        e.ID,
			Embedding: facerecognition.DecodeEmbedding(e.Embedding),
		})
	}
	medoidID, cohesion := facerecognition.MedoidAndCohesion(faces)
	if medoidID == "" {
		return
	}
	if err := d.SetPersonCoverFace(personID, medoidID, cohesion); err != nil {
		log.Error("error updating person cover face:", err)
	}
}

// matchOrNewPerson returns the person id of whichever existing face is the
// closest match above face_recognition's same-person threshold, or "" if
// none clears it - the caller creates a new person in that case. Pure
// logic, deliberately separate from processFaces' DB/model plumbing so
// it's directly unit-testable without a real database or the actual
// models.
func matchOrNewPerson(embedding []float32, existing []dao.FaceEmbedding) string {
	bestPersonID := ""
	var bestScore float32
	for _, e := range existing {
		score := facerecognition.CosineSimilarity(embedding, facerecognition.DecodeEmbedding(e.Embedding))
		if score > bestScore {
			bestScore = score
			bestPersonID = e.PersonID
		}
	}
	if bestPersonID != "" && facerecognition.IsSamePersonScore(bestScore) {
		return bestPersonID
	}
	return ""
}
