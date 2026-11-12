// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"os"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	petfinder "github.com/alonsovidales/otc/pet_finder"
	"github.com/alonsovidales/otc/session"
	textreader "github.com/alonsovidales/otc/text_reader"
)

const (
	// cVisionBackfillPage is how many photos the background pass lists at
	// a time.
	cVisionBackfillPage = 200
	// cVisionBackfillPause is the least it rests after a photo; it rests a
	// quarter of the time the photo took when that is longer (reading a
	// photo's text is about 1-5 s of a Pi 5's CPU, its original decoded
	// first).
	cVisionBackfillPause = 50 * time.Millisecond
)

// visionBackfill runs BackfillVision with ses in the background; tests
// replace it. Set in init: BackfillVision leads back to it (visionArrived).
var visionBackfill func(mg *Manager, ses *session.Session)

func init() {
	visionBackfill = func(mg *Manager, ses *session.Session) { go mg.BackfillVision(ses) }
}

// BackfillVision reads the text of the photos the device held before Text
// in photos was on, and looks for pets in them while Pets is on - once, in
// the background, from each photo's original (owner, 2026-11-12: "From the
// original"), decoded as the analysis decodes a new one (processMedia's
// stageVision): after the first sign-in (it needs the key), when either is
// turned on, and after Smart search's backfill (whose vectors say which
// photos may show a pet). It waits for their models while either is on
// (the primary downloads them; Status lists the passes meanwhile). One run
// at a time; a second caller leaves it to the one running. Only photos
// Images may show. A photo whose vector says it shows no pet is only
// recorded as looked at; one not described yet while Smart search is on is
// left for after its description. It gives way to uploads and the
// processing lanes (waitForQuiet), stops for a full reprocess or when both
// are off, and a restart goes on at the next: what was read and looked at
// is never listed again. A photo that can't be decoded is recorded as read
// and looked at, as the analysis leaves one.
func (mg *Manager) BackfillVision(ses *session.Session) {
	if mg == nil || mg.dao == nil || ses == nil || !cfg.HasSection("otc") {
		return
	}
	// Kept for the photos the slow lane may analyse before the models are
	// here (visionArrived).
	mg.holdForVision(ses)
	if !mg.visionBackfillMu.TryLock() {
		return
	}
	defer mg.visionBackfillMu.Unlock()
	textOn, petsOn := mg.TextInPhotosEnabled(), mg.PetsEnabled()
	if !textOn && !petsOn {
		return
	}
	excluded, err := mg.OutOfImagesFolders()
	if err != nil {
		log.Error("text and pets: could not read the folders kept out of Images, nothing read:", err)
		return
	}
	// Listed in Status while there is something to do (background.go).
	var bgText, bgPets *BackgroundPass
	if textOn {
		if n, err := mg.dao.CountPhotosToRead(textreader.Model, excluded); err != nil || n > 0 {
			bgText = mg.BeginBackground(BackgroundText, n)
		}
	}
	if petsOn {
		if n, err := mg.dao.CountPhotosToScanForPets(petfinder.Model, excluded); err != nil || n > 0 {
			bgPets = mg.BeginBackground(BackgroundPets, n)
		}
	}
	defer bgText.End()
	defer bgPets.End()
	if bgText == nil && bgPets == nil {
		return
	}
	// Their models first: the primary downloads them once someone wants
	// them, and a user's instance asks for them meanwhile.
	for mg.visionModel() == nil {
		if !mg.visionWanted() || mg.visionSrc == nil {
			return
		}
		mg.wantVision()
		if mg.visionModel() != nil {
			break
		}
		if mg.visionSrc.status().unavailable {
			log.Info("text and pets: their models can't be had on this device, nothing read")
			return
		}
		time.Sleep(cSmartPoll)
	}
	started := time.Now()
	read, looked, failed := 0, 0, 0
	after := ""
	for {
		textOn, petsOn = mg.TextInPhotosEnabled(), mg.PetsEnabled()
		if !textOn && !petsOn {
			return
		}
		batch, err := mg.dao.PhotosToLookAt(after, cVisionBackfillPage, textOn, textreader.Model, petsOn, petfinder.Model, cEmbedModel, excluded)
		if errors.Is(err, dao.ErrSchemaBehind) {
			log.Info("text and pets: the database has nowhere to keep them yet (the release's migration), nothing read")
			return
		}
		if err != nil {
			log.Error("text and pets: error listing the photos to read:", err)
			return
		}
		for _, w := range batch {
			hash := w.File.Hash
			after = hash
			textOn, petsOn = mg.TextInPhotosEnabled(), mg.PetsEnabled()
			needText := textOn && !w.HasText
			needPets := petsOn && !w.Scanned
			if needPets {
				var vec []float32
				if w.Vec != nil {
					vec = mg.storedVector(ses, hash, w.Vec)
				}
				if may, known := mg.petGate(vec); known && !may {
					mg.markPetScanned(hash)
					bgPets.Step(1)
					needPets = false
				} else if !known && vec == nil && mg.SmartSearchEnabled() && mg.smartEmbedder() != nil {
					// Described soon: Smart search's backfill starts this
					// one again when it is done.
					needPets = false
				}
			}
			if !needText && !needPets {
				continue
			}
			if !mg.waitForQuiet() {
				log.Info("text and pets: stopping for a reprocess after", read, "photo(s) read and", looked, "looked at for pets")
				return
			}
			if _, err := os.Stat(blobPath(hash)); err != nil {
				continue
			}
			t := time.Now()
			done := mg.beginProcessing(hash, jobBackfill)
			ok := false
			mg.safelyOn("reading", w.File, func() {
				ok = mg.processStoredStages(ses, w.File, blobPath(hash), stageVision)
			})
			done()
			if ok {
				if needText {
					read++
				}
				if needPets {
					looked++
				}
			} else {
				// Not decodable: never listed again.
				failed++
				if needText {
					if _, err := mg.keepText(ses, hash, nil, mg.texts.turn.Load()); err != nil {
						log.Debug("text in photos: could not record", hash, "as read:", err)
					}
				}
				if needPets {
					mg.markPetScanned(hash)
				}
			}
			if needText {
				bgText.Step(1)
			}
			if needPets {
				bgPets.Step(1)
			}
			time.Sleep(max(cVisionBackfillPause, time.Since(t)/4))
		}
		if len(batch) < cVisionBackfillPage {
			break
		}
	}
	if read > 0 || looked > 0 || failed > 0 {
		log.Info("text and pets: read", read, "photo(s), looked at", looked, "for pets, in", time.Since(started).Round(time.Second), "-", failed, "not decodable")
	}
}
