// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	"os"
	"strings"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	imagesquality "github.com/alonsovidales/otc/images_quality"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/modelserver"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// On this day (Memories): while it is on (memories.go's switch) every photo
// gets a photo_quality row - how sharp and well exposed it is, NIMA's score
// when the model is there, the size of its big thumbnail and whether its
// metadata names a camera - when the slow lane analyses it (storeQuality),
// and what the device held before gets one in the background
// (BackfillQuality). Everything is measured from the big thumbnail (at most
// 1000 px), never the original: the same scale for every photo, the same
// pixels for the lane and the backfill, and a few kilobytes to decrypt.

const (
	// cQualityBackfillPage is how many photos the backfill lists at a time.
	cQualityBackfillPage = 500
	// cQualityBackfillPause is the least the backfill rests after a photo;
	// it rests as long as measuring took when that was longer (on a Pi 5
	// about 0.1 s: a thumbnail's decode, the model and the measures).
	cQualityBackfillPause = 30 * time.Millisecond
	// cQualityTimeout bounds a photo's turn at the model, the wait included
	// (a child's photos queue behind the other instances' at the primary).
	cQualityTimeout = time.Minute
)

// qualityModelPath is [quality] model-path when the section is there and
// so is the file; "" otherwise (logged once at start).
func qualityModelPath() string {
	if !cfg.HasSection("quality") {
		log.Info("photo quality model not configured ([quality] section): On this day measures photos without it")
		return ""
	}
	p := cfg.GetStr("quality", "model-path")
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		log.Info("photo quality model not available: On this day measures photos without it until it is there")
		return ""
	}
	return p
}

// loadQualityModel is the model a primary loads at start: nil when there is
// none, or it doesn't load (logged; the device works on without it).
func loadQualityModel() modelserver.AestheticScorer {
	p := qualityModelPath()
	if p == "" {
		return nil
	}
	started := time.Now()
	n, err := imagesquality.NewNIMA(p, false)
	if err != nil {
		log.Error("could not load the photo quality model, On this day measures photos without it:", err)
		return nil
	}
	log.Info("photo quality model loaded in", time.Since(started).Round(time.Millisecond))
	return n
}

// cameraOf is whether ex names a camera: a phone's or a camera's photo
// does, a screenshot, a picture saved from the web or an edited export
// mostly not. nil ex (no metadata) is no camera.
func cameraOf(ex *exifinfo.Info) *bool {
	named := ex != nil && (strings.TrimSpace(ex.CameraMake) != "" || strings.TrimSpace(ex.CameraModel) != "")
	return &named
}

// qualityOf measures img, a photo's big thumbnail.
func (mg *Manager) qualityOf(img image.Image, camera *bool) dao.PhotoQuality {
	light := imagesquality.Exposure(img)
	b := img.Bounds()
	q := dao.PhotoQuality{
		Sharpness:  imagesquality.Sharpness(img),
		Dark:       light.Dark,
		Bright:     light.Bright,
		Brightness: light.Mean,
		Width:      b.Dx(),
		Height:     b.Dy(),
		Camera:     camera,
	}
	if mg.aesthetic != nil {
		ctx, cancel := context.WithTimeout(context.Background(), cQualityTimeout)
		score, err := mg.aesthetic.AestheticResized(ctx, imagesquality.Resize(img))
		cancel()
		if err == nil {
			q.Aesthetic = &score
		} else if !errors.Is(err, modelserver.ErrNoAesthetic) {
			log.Debug("on this day: the quality model failed on a photo, measured without it:", err)
		}
	}
	return q
}

// readThumbnailBytes is hash's small thumbnail, or its big one when it has
// no small one: enough to tell two shots apart (memories.go).
func readThumbnailBytes(ses *session.Session, hash string) ([]byte, error) {
	data, err := blobstore.ReadAll(smallThumbnailPath(hash), ses)
	if err == nil {
		return data, nil
	}
	return blobstore.ReadAll(thumbnailPath(hash), ses)
}

// decodeThumbnail decodes a thumbnail's JPEG.
func decodeThumbnail(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// measure reads hash's big thumbnail and measures it.
func (mg *Manager) measure(ses *session.Session, hash string, camera *bool) (dao.PhotoQuality, error) {
	data, err := blobstore.ReadAll(thumbnailPath(hash), ses)
	if err != nil {
		return dao.PhotoQuality{}, err
	}
	img, err := decodeThumbnail(data)
	if err != nil {
		return dao.PhotoQuality{}, err
	}
	return mg.qualityOf(img, camera), nil
}

// storeQuality measures a photo the slow lane has just analysed, from the
// thumbnail the fast lane wrote, with the metadata processing read - while
// On this day is on. A failure leaves no row: the next start's backfill
// measures it.
func (mg *Manager) storeQuality(ses *session.Session, file *pb.File, ex *exifinfo.Info) {
	if ses == nil || !mg.MemoriesEnabled() {
		return
	}
	q, err := mg.measure(ses, file.Hash, cameraOf(ex))
	if err != nil {
		log.Debug("on this day: could not measure", file.Hash, ":", err)
		return
	}
	if err := mg.dao.SetPhotoQuality(file.Hash, q); err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
		log.Error("on this day: could not record how", file.Hash, "looks:", err)
		return
	}
	mg.memories.changed()
}

// cameraOfStored reads whether a stored photo's metadata names a camera:
// up to its EXIF, as Places' backfill reads it. nil when the blob can't be
// read this time.
func (mg *Manager) cameraOfStored(ses *session.Session, file *pb.File) *bool {
	blob, err := blobstore.Open(blobPath(file.Hash), ses)
	if err != nil {
		return nil
	}
	defer blob.Close()
	ex, err := mg.exifOfBlob(blob, file, mg.shownWholeLimit())
	if err != nil {
		ex = nil
	}
	return cameraOf(ex)
}

// BackfillQuality measures every photo the device held before On this day
// (or was turned on), once, in the background: after the first sign-in (it
// needs the key), and when On this day is turned on. One run at a time; a
// second caller leaves it to the one running. Only photos Images may show,
// with a thumbnail; one measured without the quality model is measured
// again once the model is there. It gives way to uploads and the processing
// lanes (waitForQuiet), stops for a full reprocess or when On this day is
// turned off, and a restart before the end goes on at the next: what has a
// row is never listed again.
func (mg *Manager) BackfillQuality(ses *session.Session) {
	if mg == nil || mg.dao == nil || ses == nil || !cfg.HasSection("otc") {
		return
	}
	st := &mg.memories
	if !st.backfillMu.TryLock() {
		return
	}
	defer st.backfillMu.Unlock()
	if !mg.MemoriesEnabled() {
		return
	}
	st.measuring.Store(true)
	defer st.measuring.Store(false)
	excluded, err := mg.OutOfImagesFolders()
	if err != nil {
		log.Error("on this day: could not read the folders kept out of Images, nothing measured:", err)
		return
	}
	withoutAesthetic := mg.aesthetic != nil
	// Listed in Status while there is something to measure (background.go).
	var bg *BackgroundPass
	if n, err := mg.dao.CountPhotosToScore(withoutAesthetic, excluded); err != nil || n > 0 {
		bg = mg.BeginBackground(BackgroundQuality, n)
	}
	defer bg.End()
	started := time.Now()
	measured, failed := 0, 0
	after := ""
	for {
		batch, err := mg.dao.PhotosToScore(after, cQualityBackfillPage, withoutAesthetic, excluded)
		if errors.Is(err, dao.ErrSchemaBehind) {
			log.Info("on this day: the database has nowhere to keep the measures yet (the release's migration), nothing measured")
			return
		}
		if err != nil {
			log.Error("on this day: error listing the photos to measure:", err)
			return
		}
		for _, file := range batch {
			after = file.Hash
			bg.Step(1)
			if !mg.MemoriesEnabled() {
				log.Info("on this day: turned off, the measuring stops after", measured, "photo(s)")
				return
			}
			if !mg.waitForQuiet() {
				log.Info("on this day: stopping for a reprocess after", measured, "photo(s)")
				return
			}
			if _, err := os.Stat(thumbnailPath(file.Hash)); err != nil {
				continue
			}
			done := mg.beginProcessing(file.Hash, jobThumbFix)
			t := time.Now()
			q, err := mg.measure(ses, file.Hash, mg.cameraOfStored(ses, file))
			done()
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					failed++
					log.Debug("on this day: could not measure", file.Hash, ":", err)
				}
				continue
			}
			if err := mg.dao.SetPhotoQuality(file.Hash, q); err != nil {
				log.Error("on this day: could not record a photo's measures, stopping:", err)
				return
			}
			measured++
			if measured%cQualityBackfillPage == 0 {
				mg.memories.changed()
			}
			time.Sleep(max(cQualityBackfillPause, time.Since(t)))
		}
		if len(batch) < cQualityBackfillPage {
			break
		}
	}
	if measured > 0 || failed > 0 {
		mg.memories.changed()
		log.Info("on this day: measured", measured, "photo(s) in", time.Since(started).Round(time.Second), "-", failed, "to measure again at the next start")
	}
}
