// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"io"
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

// cOrphanMinAge: what was modified this little before the process started
// is left alone too (the previous run may have been at it until the last
// moment; the next start takes it).
const cOrphanMinAge = time.Minute

// cOrphanBatch is how many directory entries the sweep reads at a time.
// The storage folder is flat - a blob and two thumbnails per file - and
// os.ReadDir read it whole and sorted the names: 600k entries took 132 MB
// and half a second on an M-series Mac, seconds on a Pi's cold disk.
const cOrphanBatch = 4096

// AfterListening is the start-up work that waits until bin/otc.go has the
// listeners up, so the device answers while it runs: the sweep of what
// interrupted work left (sweepOrphanedStorage), in the background.
func (mg *Manager) AfterListening() {
	go mg.sweepOrphanedStorage()
}

// sweepOrphanedStorage looks only at what was last modified before this
// process started (mg.started): uploads, archives and galleries this run
// makes - which may be under way by now - are always newer.
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
	started := mg.started
	if started.IsZero() {
		started = time.Now()
	}
	n, freed := sweepOrphans(cfg.GetStr("otc", "storage-path"), cfg.GetStr("otc", "unenc-storage-path"), links, started.Add(-cOrphanMinAge))
	if n > 0 {
		log.Info("removed", n, "file(s) left by interrupted work,", freed>>20, "MB")
	}
}

// eachEntry calls fn with dir's entries, cOrphanBatch at a time, in the
// directory's own order. A folder that can't be read is logged by what.
func eachEntry(dir, what string, fn func(e fs.DirEntry)) {
	f, err := os.Open(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Error("could not list the", what+":", err)
		}
		return
	}
	defer f.Close()
	for {
		entries, err := f.ReadDir(cOrphanBatch)
		for _, e := range entries {
			fn(e)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Error("could not finish listing the", what+":", err)
			}
			return
		}
	}
}

// sweepOrphans is sweepOrphanedStorage without the config and the
// database. Only the top level of each folder is looked at: storage also
// holds the other users' instances (user_<uuid>/), each sweeping its own.
// links nil leaves archives and galleries alone; before is the time a
// leftover was last modified before.
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
		eachEntry(storage, "storage folder", func(e fs.DirEntry) { // i18n-ignore: names a folder in the log
			name := e.Name()
			// Temp blobs (blobstore.Create, writeBlob), and archives with
			// no row: blobs (64 hex digits) and thumbnails never match.
			if !e.Type().IsRegular() || !(strings.HasPrefix(name, ".blob-") || strings.HasPrefix(name, ".upload-") || orphanLink(name)) {
				return
			}
			if size, ok := old(e); ok {
				remove(filepath.Join(storage, name), size, false)
			}
		})
		// Gallery copies with no row (buildSharedGallery stopped before
		// recording it).
		shared := filepath.Join(storage, "shared")
		eachEntry(shared, "shared galleries", func(e fs.DirEntry) { // i18n-ignore: names a folder in the log
			if !e.IsDir() || !orphanLink(e.Name()) {
				return
			}
			if _, ok := old(e); ok {
				dir := filepath.Join(shared, e.Name())
				remove(dir, dirSize(dir), true)
			}
		})
	}
	if unenc != "" {
		// A post's video copies being written (post_export.go).
		eachEntry(unenc, "posts' folder", func(e fs.DirEntry) { // i18n-ignore: names a folder in the log
			if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), ".post-") {
				return
			}
			if size, ok := old(e); ok {
				remove(filepath.Join(unenc, e.Name()), size, false)
			}
		})
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
