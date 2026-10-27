// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
)

// A post's files - <hash> and <hash>_thumbnail in unenc-storage-path - are
// plaintext copies of the owner's photos and videos, and they are written
// before the post's row exists. A post that failed half-way (a video that
// couldn't be re-encoded, a thumbnail that never came, a full disk) used
// to leave the copies it had made behind for good: nothing names them, no
// storage limit counts them, and they are the owner's photos unencrypted.
// NewPublication now takes back what it wrote (postFiles.discard), and
// once at every start SweepUnusedPostFiles removes what an earlier run
// left (a crash between the two).

// postsInFlight counts, per hash, the posts being made here that wrote, or
// will name, its files and whose row isn't stored yet. The cleanups below
// leave those files alone: two posts of one photo at once, one of them
// failing, must not take the file the other is about to name. Every
// decision to remove a post file holds the lock, so a post marks its
// hashes before it writes and nothing removes one in between.
var postsInFlight = struct {
	sync.Mutex
	n map[string]int
}{n: map[string]int{}}

// postFiles is what one post being made marks and has written in dir.
type postFiles struct {
	dir    string
	hashes []string
}

// hold marks hash as one of this post's, before its files are written.
func (p *postFiles) hold(hash string) {
	if !dao.IsContentHash(hash) {
		return
	}
	for _, h := range p.hashes {
		if h == hash {
			return
		}
	}
	postsInFlight.Lock()
	postsInFlight.n[hash]++
	postsInFlight.Unlock()
	p.hashes = append(p.hashes, hash)
}

// release lets go of the post's hashes once its row is stored: from then
// on the row is what keeps its files.
func (p *postFiles) release() {
	postsInFlight.Lock()
	defer postsInFlight.Unlock()
	p.releaseLocked()
}

func (p *postFiles) releaseLocked() {
	for _, h := range p.hashes {
		if postsInFlight.n[h]--; postsInFlight.n[h] <= 0 {
			delete(postsInFlight.n, h)
		}
	}
	p.hashes = nil
}

// discard is release for a post that wasn't made: what it wrote goes, but
// a file another post names, or another post being made holds, stays. A
// file whose use can't be told stays too.
func (p *postFiles) discard(sc *Social) {
	postsInFlight.Lock()
	defer postsInFlight.Unlock()
	hashes := p.hashes
	p.releaseLocked()
	for _, h := range hashes {
		if postsInFlight.n[h] > 0 {
			continue
		}
		if inUse, err := sc.dao.SocialHashInUse(h); err != nil || inUse {
			continue
		}
		removePostFile(filepath.Join(p.dir, h))
		removePostFile(filepath.Join(p.dir, h+cThumbnailSuffix))
	}
}

const cThumbnailSuffix = "_thumbnail"

func removePostFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Error("could not remove a post's file:", filepath.Base(path), err)
	}
}

// cUnusedPostFileAge: a post file no post names is removed only once it
// is this old. Every file a post being made relies on is one it wrote
// itself, just now (postFiles), and a friend's sync - which may find a
// file already there and use it - only starts once the sweep is done.
const cUnusedPostFileAge = 24 * time.Hour

// cSweepBatch is how many directory entries the sweeps read at a time:
// the folder is flat, and read whole and sorted its names took memory
// for every file in it.
const cSweepBatch = 4096

// SweepUnusedPostFiles removes from dir (unenc-storage-path) the post files
// - <hash>, <hash>_thumbnail - that no post here names, own or a friend's
// (social_publications_files is the only table naming them; the alerts'
// thumbnails are their posts'), older than cUnusedPostFileAge. Anything
// else in dir is left alone. Run once at start, before the first friend
// sync (websocket's backgroundWorker); when the posts can't be read,
// nothing is removed.
func (sc *Social) SweepUnusedPostFiles(dir string) {
	if dir == "" {
		return
	}
	inUse, err := sc.dao.SocialMediaHashes()
	if err != nil {
		log.Error("could not list the posts' files, unused copies stay for now:", err)
		return
	}
	n, freed := sweepPostFiles(dir, inUse, time.Now().Add(-cUnusedPostFileAge), sc.dao.SocialHashInUse)
	if n > 0 {
		log.Info("removed", n, "file(s) of posts that were never made,", freed>>20, "MB")
	}
}

// sweepPostFiles is SweepUnusedPostFiles without the database: inUse the
// hashes posts name, before the time a file must be older than, stillUsed
// the check made again right before a file goes (a post stored since the
// list was read).
func sweepPostFiles(dir string, inUse map[string]bool, before time.Time, stillUsed func(hash string) (bool, error)) (n int, freed int64) {
	f, err := os.Open(dir)
	if err != nil {
		log.Error("could not look for unused post files:", err)
		return 0, 0
	}
	defer f.Close()
	for {
		entries, err := f.ReadDir(cSweepBatch)
		for _, e := range entries {
			hash := strings.TrimSuffix(e.Name(), cThumbnailSuffix)
			if !e.Type().IsRegular() || !dao.IsContentHash(hash) || inUse[hash] {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil || !info.ModTime().Before(before) {
				continue
			}
			if removeUnusedPostFile(filepath.Join(dir, e.Name()), hash, before, stillUsed) {
				n++
				freed += info.Size()
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Error("could not finish looking for unused post files:", err)
			}
			return n, freed
		}
	}
}

// removeUnusedPostFile removes path, a file of hash's, when no post being
// made holds hash and no post names it now, and it is still older than
// before; it says whether it did.
func removeUnusedPostFile(path, hash string, before time.Time, stillUsed func(string) (bool, error)) bool {
	postsInFlight.Lock()
	defer postsInFlight.Unlock()
	if postsInFlight.n[hash] > 0 {
		return false
	}
	if used, err := stillUsed(hash); err != nil || used {
		return false
	}
	// Written again meanwhile (a post of the same content): not unused.
	if info, err := os.Stat(path); err != nil || !info.ModTime().Before(before) {
		return false
	}
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Error("could not remove an unused post file:", filepath.Base(path), err)
		}
		return false
	}
	return true
}
