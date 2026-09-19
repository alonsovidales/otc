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

			if _, statErr := os.Stat(fmt.Sprintf("%s/%s_thumbnail", storagePath, file.Hash)); statErr == nil {
				continue
			}

			log.Debug("thumbnail backfill: rebuilding", file.Path)
			mg.reprocessOneFile(ses, file, storagePath)
			repaired++
			time.Sleep(cBackfillPause)
		}
	}

	if repaired > 0 {
		log.Info("thumbnail backfill: rebuilt", repaired, "of", scanned, "file(s) in", time.Since(started))
	} else {
		log.Debug("thumbnail backfill: nothing to do across", scanned, "file(s)")
	}
}

func (mg *Manager) isReprocessing() bool {
	mg.reprocessMu.Lock()
	defer mg.reprocessMu.Unlock()

	return mg.reprocessing
}
