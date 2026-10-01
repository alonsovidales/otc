// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"os"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
)

const (
	// cBackfillBatchSize pages through the library the same way issue
	// #73's reprocess does.
	cBackfillBatchSize = 200
	// cBackfillPause is breathing room between two files. This runs
	// unattended on a device that is also serving the person using it,
	// and a file costs a decrypt plus ffmpeg plus the tagging model -
	// there is no hurry here, and being invisible matters more than
	// being quick.
	cBackfillPause = 500 * time.Millisecond
)

// BackfillMissingThumbnails finds media whose thumbnail isn't on disk and
// runs it back through the same pipeline a fresh upload does.
//
// Two separate things put files in that state, and this fixes both:
//
//   - A video narrower than max-thumbnail-width-px never got a thumbnail
//     written at all (the "only resize when needed" bug that made posting
//     such a video fail outright - fixed in UploadFile, but that fix can't
//     reach into what's already stored).
//   - Upload-time processing happens in a background goroutine, which a
//     restart simply loses. A device restarted during a big first sync -
//     exactly when there's most to process - leaves whatever was queued
//     unprocessed forever, with no thumbnail and no tags. Measured on a
//     real library: 36% of videos and 2% of photos.
//
// A missing thumbnail is a blank tile in the gallery, and because the
// same pass writes tags, those files were also invisible to search. Both
// come back.
//
// Called once per process after the first sign-in (it needs the owner's
// key to read anything), and cheap when there's nothing to do: one stat
// per file and no work at all if every thumbnail is present. That also
// makes it the durable half of upload processing - anything a restart
// drops is picked up the next time someone signs in.
func (mg *Manager) BackfillMissingThumbnails(ses *session.Session) {
	// cfg.GetStr is fatal before cfg.Init, and this runs on its own
	// goroutine - so without this check a process that never loaded a
	// config (a test binary, say) is taken down by a background repair
	// job rather than simply not running one. Nothing here can work
	// without a storage path anyway.
	if !cfg.HasSection("otc") {
		return
	}
	storagePath := cfg.GetStr("otc", "storage-path")
	lastHash := ""
	scanned, repaired := 0, 0
	started := time.Now()
	// Uploads the lanes are still on (ResumePendingAnalysis ran first).
	pending := mg.pendingAnalysisSet()

	for {
		batch, err := mg.dao.ListMediaForReprocess(lastHash, cBackfillBatchSize)
		if err != nil {
			log.Error("thumbnail backfill: error listing media:", err)
			return
		}
		if len(batch) == 0 {
			break
		}

		for _, file := range batch {
			lastHash = file.Hash
			scanned++

			// Issue #73's full reprocess rewrites everything this would,
			// so stepping aside avoids two jobs decrypting the same
			// files at once on a machine that can't spare it.
			if mg.isReprocessing() {
				log.Info("thumbnail backfill: a full reprocess is running, stopping after", repaired, "file(s)")
				return
			}

			if pending[file.Hash] {
				continue
			}
			thumb := fmt.Sprintf("%s/%s_thumbnail", storagePath, file.Hash)
			if _, statErr := os.Stat(thumb); statErr == nil {
				continue
			}
			// Content that already failed once (a GIF, JPEG 2000, TIFF or
			// PSD the decoder doesn't know, a damaged file) fails the same
			// way every time: it used to be retried - and alerted about -
			// on every restart, a burst of work on a device that just
			// came back. The marker is per content hash, so a new version
			// of the file is tried afresh.
			marker := thumb[:len(thumb)-len("_thumbnail")] + cNoThumbnailSuffix
			if raw, readErr := os.ReadFile(marker); readErr == nil && string(raw) == cDecoders {
				continue
			}

			log.Debug("thumbnail backfill: rebuilding", file.Path)
			// The marker goes down first and comes off once there's a
			// thumbnail (issue #165): a file that takes the process down
			// used to be retried at every start - a crash loop - because
			// the marker was only written after processing returned.
			_ = os.WriteFile(marker, []byte(cDecoders), 0o600) // perms: rw-------
			mg.safely("making a thumbnail for", file.Path, func() { mg.reprocessOneFile(ses, file, storagePath) })
			repaired++
			if _, statErr := os.Stat(thumb); statErr == nil {
				_ = os.Remove(marker)
			}
			time.Sleep(cBackfillPause)
		}
	}

	if repaired > 0 {
		log.Info("thumbnail backfill: rebuilt", repaired, "of", scanned, "file(s) in", time.Since(started))
	} else {
		log.Debug("thumbnail backfill: nothing to do across", scanned, "file(s)")
	}
}

// cNoThumbnailSuffix marks content the backfill could not make a
// thumbnail for (next to its blob, <hash>.nothumb), so it isn't retried on
// every restart.
const cNoThumbnailSuffix = ".nothumb"

// cDecoders names what this build can make previews from; a marker written
// by a build that could do less is retried once (release 31's markers are
// empty, so the GIF/WebP/BMP/TIFF they recorded get another go).
const cDecoders = "jpeg,png,gif,webp,bmp,tiff,heic,ffmpeg-image,short-video"

func (mg *Manager) isReprocessing() bool {
	mg.reprocessMu.Lock()
	defer mg.reprocessMu.Unlock()

	return mg.reprocessing
}
