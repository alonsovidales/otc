// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
)

// Work interrupted by a restart (an update, a crash, the OOM killer) left
// files that only its memory, or a row it never got to write, knew about:
// a chunked upload's temp blob (GBs for a video, and one more per retry),
// a thumbnail's or a share archive's, an archive or a gallery copy whose
// shared_links row was never inserted, a post's plaintext video copy.
// Nothing ever listed the storage folder, so they stayed until the RAID
// filled. sweepOrphanedStorage removes them once per start.

// cOrphanMinAge: anything this recent is left alone - nothing of this
// process writes before the sweep, this is only a safety net.
const cOrphanMinAge = time.Minute

// sweepOrphanedStorage runs at start, before anything of this process can
// be writing to storage (see initRest).
func (mg *Manager) sweepOrphanedStorage() {
	if !cfg.HasSection("otc") || mg.dao == nil {
		return
	}
	// The archives and galleries that are meant to be there. Unknown when
	// the query fails: then none of them is touched.
	var links map[string]bool
	if rows, err := mg.dao.ListSharedLinks(); err != nil {
		log.Error("could not list the share links, their leftovers stay for now:", err)
	} else {
		links = make(map[string]bool, len(rows))
		for _, r := range rows {
			links[r.Uuid] = true
		}
	}
	n, freed := sweepOrphans(cfg.GetStr("otc", "storage-path"), cfg.GetStr("otc", "unenc-storage-path"), links, time.Now().Add(-cOrphanMinAge))
	if n > 0 {
		log.Info("removed", n, "file(s) left by interrupted work,", freed>>20, "MB")
	}
}

// sweepOrphans is sweepOrphanedStorage without the config and the
// database. Only the top level of each folder is looked at: storage also
// holds the other users' instances (user_<uuid>/), each sweeping its own.
// links nil leaves archives and galleries alone.
func sweepOrphans(storage, unenc string, links map[string]bool, before time.Time) (n int, freed int64) {
	remove := func(path string, size int64, all bool) {
		var err error
		if all {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			log.Error("could not remove a leftover of interrupted work:", filepath.Base(path), err)
			return
		}
		n++
		freed += size
	}
	old := func(e fs.DirEntry) (int64, bool) {
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(before) {
			return 0, false
		}
		return info.Size(), true
	}
	orphanLink := func(name string) bool {
		return links != nil && galleryUUID.MatchString(name) && !links[name]
	}

	if storage != "" {
		entries, err := os.ReadDir(storage)
		if err != nil {
			log.Error("could not list the storage folder:", err)
		}
		for _, e := range entries {
			name := e.Name()
			// Temp blobs (blobstore.Create, writeBlob), and archives with
			// no row: blobs (64 hex digits) and thumbnails never match.
			if !e.Type().IsRegular() || !(strings.HasPrefix(name, ".blob-") || strings.HasPrefix(name, ".upload-") || orphanLink(name)) {
				continue
			}
			if size, ok := old(e); ok {
				remove(filepath.Join(storage, name), size, false)
			}
		}
		// Gallery copies with no row (buildSharedGallery stopped before
		// recording it).
		shared := filepath.Join(storage, "shared")
		entries, _ = os.ReadDir(shared)
		for _, e := range entries {
			if !e.IsDir() || !orphanLink(e.Name()) {
				continue
			}
			if _, ok := old(e); ok {
				dir := filepath.Join(shared, e.Name())
				remove(dir, dirSize(dir), true)
			}
		}
	}
	if unenc != "" {
		// A post's video copies being written (post_export.go).
		entries, _ := os.ReadDir(unenc)
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), ".post-") {
				continue
			}
			if size, ok := old(e); ok {
				remove(filepath.Join(unenc, e.Name()), size, false)
			}
		}
	}
	return n, freed
}

func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
