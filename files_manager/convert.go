// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
)

// ConvertToSegments rewrites every blob and thumbnail still in the old
// whole-seal encryption into the segmented format (see blobstore), so the
// device never again has to hold a whole file in memory to read part of
// it. It needs the owner's key, so it runs once per process after the
// first sign-in, before the thumbnail backfill (which then reads the new
// format). One file at a time, under the file's own lock and the download
// memory budget; a file converted is renamed into place, so a reader or an
// interrupted run never sees half of one. Files that don't open with the
// owner's key (share-link archives have their own) are left alone.
// cConvertMax: larger old-format files are left as they are (see below).
const cConvertMax = 512 << 20

func (mg *Manager) ConvertToSegments(ses *session.Session) {
	if !cfg.HasSection("otc") {
		return
	}
	dir := cfg.GetStr("otc", "storage-path")
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Error("segment conversion: listing", dir, ":", err)
		return
	}
	started := time.Now()
	converted, skipped, tooBig := 0, 0, 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || strings.HasSuffix(name, cNoThumbnailSuffix) {
			continue
		}
		path := filepath.Join(dir, name)
		if seg, err := blobstore.IsSegmented(path); err != nil || seg {
			continue
		}
		hash := strings.TrimSuffix(name, "_thumbnail")
		info, err := e.Info()
		if err != nil {
			continue
		}
		// The old format can only be opened whole (one GCM seal): a file
		// this big would need twice its size in memory to convert - enough
		// to bring the device down. It stays as it is and reads as before.
		if info.Size() > cConvertMax {
			tooBig++
			continue
		}
		release := func() {}
		if mg.contentBudget != nil {
			release = mg.contentBudget.acquire(info.Size() * 2)
		}
		unlock := lockBlob(hash)
		ok, err := blobstore.Convert(path, ses)
		unlock()
		release()
		if err != nil {
			skipped++
			log.Debug("segment conversion: left", name, "as it is:", err)
			continue
		}
		if ok {
			converted++
		}
	}
	if converted > 0 || skipped > 0 || tooBig > 0 {
		log.Info("segment conversion: converted", converted, "file(s), left", skipped, "unreadable with the owner's key and", tooBig, "over", cConvertMax>>20, "MB, in", time.Since(started))
	}
}
