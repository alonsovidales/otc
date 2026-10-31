// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"sync"
	"time"

	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	imagesquality "github.com/alonsovidales/otc/images_quality"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
)

// The face People shows for a person when the owner chose none (owner,
// 2026-10-31: "it would be better to get the best looking one"): the
// best-looking of their faces that is clearly them. Every face's looks are
// measured when it is found (faceQuality: sharp, large, well lit, frontal),
// dao.BestFaces ranks a person's faces by them and by their photo's own
// score, and chooseCoverLocked keeps the first whose embedding is close to
// the person's references - as close on average as their most typical face
// is, give or take cCoverTypicality, and never below the same-person
// threshold - so a sharp stranger misfiled under them, or a poster in the
// background, never becomes their face. None close enough (or nothing
// measured yet): their most typical face (the medoid), as before. A face the
// owner chose stays (dao.SetPersonCoverFace).
//
// Faces found before release 157 are measured once, in the background
// after a sign-in (BackfillFaceQuality: their crops are encrypted), and then
// everyone's face is chosen again.
const (
	// cCoverCandidates: how many of a person's best-looking faces are
	// checked for being clearly them.
	cCoverCandidates = 30
	// cCoverTypicality: a candidate's average similarity to the person's
	// references must reach this share of their medoid's (the cohesion).
	cCoverTypicality = 0.85
	// cFaceQualityPage is how many faces the pass reads at a time, and
	// cFaceQualityPause its pause after each (a crop of a few KB to decrypt
	// and decode).
	cFaceQualityPage  = 500
	cFaceQualityPause = 5 * time.Millisecond
)

var faceQualityMu sync.Mutex

// faceQuality is how good a face looks, 0 to 1, from its crop (a small
// JPEG) and its box in the photo; 0 for a crop that can't be read.
func faceQuality(crop []byte, w, h int) float64 {
	img, err := jpeg.Decode(bytes.NewReader(crop))
	if err != nil {
		return 0
	}
	return imagesquality.FaceQuality(img, min(w, h))
}

// chooseCoverLocked works out the person's face from their references p
// (see above) and stores it with their cohesion. Without a session (nothing
// to decrypt the candidates with) it is the medoid. The caller holds
// faceRefsMu.
func (mg *Manager) chooseCoverLocked(ses *session.Session, personID string, p *personFaceRefs) {
	if p == nil {
		return
	}
	medoidID, cohesion := facerecognition.MedoidAndCohesion(p.identified())
	if medoidID == "" {
		return
	}
	coverID := medoidID
	p.coverKnown, p.coverQuality = true, 0
	if ses != nil {
		if id, quality, ok := mg.bestLookingFace(ses, personID, p, cohesion); ok {
			coverID, p.coverQuality = id, quality
		}
	}
	if err := mg.dao.SetPersonCoverFace(personID, coverID, cohesion); err != nil {
		log.Error("error updating person cover face:", err)
	}
}

// bestLookingFace is the best-looking of personID's measured faces that is
// clearly them (see above), and its quality.
func (mg *Manager) bestLookingFace(ses *session.Session, personID string, p *personFaceRefs, cohesion float32) (string, float64, bool) {
	candidates, err := mg.dao.BestFaces(personID, cCoverCandidates)
	if err != nil {
		if !errors.Is(err, dao.ErrSchemaBehind) {
			log.Error("error ranking a person's faces:", err)
		}
		return "", 0, false
	}
	for _, c := range candidates {
		plain, err := ses.Decrypt(c.Embedding)
		if err != nil {
			continue
		}
		if clearlyThem(facerecognition.DecodeEmbedding(plain), p, cohesion) {
			return c.ID, c.Quality, true
		}
	}
	return "", 0, false
}

// clearlyThem says whether a face with this embedding is as close to the
// person's references, on average, as cCoverTypicality of their medoid is,
// and at least as close as a face must be to join them.
func clearlyThem(emb []float32, p *personFaceRefs, cohesion float32) bool {
	if len(emb) == 0 || len(p.refs) == 0 {
		return false
	}
	var total float32
	n := 0
	for _, r := range p.refs {
		if len(r.emb) != len(emb) {
			continue
		}
		total += facerecognition.CosineSimilarity(emb, r.emb)
		n++
	}
	if n == 0 {
		return false
	}
	avg := total / float32(n)
	return facerecognition.IsSamePersonScore(avg) && avg >= cCoverTypicality*cohesion
}

// coverMayChange says whether a face just stored for a person can change
// their face: their references changed (kept), or it looks better than
// the one chosen, or nothing was chosen in this process yet.
func coverMayChange(p *personFaceRefs, kept bool, quality float64) bool {
	return p != nil && (kept || !p.coverKnown || quality > p.coverQuality)
}

// BackfillFaceQuality measures the faces found before release 157 and then
// chooses everyone's face again - once per database (settings.
// face_quality_done), in the background after a sign-in, behind uploads
// and the processing lanes (waitForQuiet). A restart goes on with the faces
// still unmeasured.
func (mg *Manager) BackfillFaceQuality(ses *session.Session) {
	if mg == nil || mg.dao == nil || ses == nil {
		return
	}
	if !faceQualityMu.TryLock() {
		return
	}
	defer faceQualityMu.Unlock()
	done, err := mg.dao.FaceQualityDone()
	if errors.Is(err, dao.ErrSchemaBehind) {
		log.Info("faces: the database has no place for their looks yet (the release's migration), nothing measured")
		return
	}
	if err != nil {
		log.Error("faces: could not tell whether their looks were measured:", err)
		return
	}
	if done {
		return
	}
	total, _ := mg.dao.CountFacesToMeasure()
	pass := mg.BeginBackground(BackgroundFaces, total)
	defer pass.End()
	started := time.Now()
	measured, after := 0, ""
	for {
		batch, err := mg.dao.FacesToMeasure(after, cFaceQualityPage)
		if err != nil {
			log.Error("faces: error listing the faces to measure:", err)
			return
		}
		if len(batch) == 0 {
			break
		}
		for _, f := range batch {
			if !mg.waitForQuiet() {
				log.Info("faces: stopping for a reprocess, to go on at the next start")
				return
			}
			after = f.ID
			pass.Step(1)
			quality := 0.0
			if crop, err := ses.Decrypt(f.Thumbnail); err == nil {
				quality = faceQuality(crop, f.W, f.H)
			}
			if err := mg.dao.SetFaceQuality(f.ID, quality); err != nil {
				log.Error("faces: could not store a face's looks:", err)
				return
			}
			measured++
			time.Sleep(cFaceQualityPause)
		}
	}
	people, err := mg.dao.PeopleWithDeviceCover()
	if err != nil {
		log.Error("faces: could not list the people:", err)
		return
	}
	for _, id := range people {
		if !mg.waitForQuiet() {
			log.Info("faces: stopping for a reprocess, to go on at the next start")
			return
		}
		mg.faceRefsMu.Lock()
		refs, err := mg.loadFaceRefsLocked(ses)
		if err == nil {
			mg.chooseCoverLocked(ses, id, refs[id])
		}
		mg.faceRefsMu.Unlock()
		if err != nil {
			log.Error("faces: could not read the references:", err)
			return
		}
	}
	if err := mg.dao.SetFaceQualityDone(); err != nil {
		log.Error("faces: could not record the pass as done:", err)
		return
	}
	log.Info(fmt.Sprintf("faces: measured %d face(s) and chose the face of %d person(s) again in %s",
		measured, len(people), time.Since(started).Round(time.Second))) // i18n-ignore: a log line
}
