// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
)

// Content being processed at two deaths of the process (runstate.go) is
// set aside: taken off processing - the analysis, the backfill, Reprocess,
// the thumbnail fixes - and listed in <storage>/.set-aside with when and
// under which binary. It is tried again, by itself, once the binary is
// another (an update may have fixed what killed it) or after
// cSetAsideRetry: at the start, retrySetAside puts it back in
// pending_analysis, which the first sign-in resumes like any interrupted
// upload. If it kills the process twice again, it is set aside again.

// cSetAsideFile is the list, in the storage path (on the disks, not the SD
// card: it names content hashes).
const cSetAsideFile = ".set-aside"

// cSetAsideRetry: set-aside content is tried again after this long even
// under the same binary.
const cSetAsideRetry = 30 * 24 * time.Hour

// setAsideEntry is one hash of the list.
type setAsideEntry struct {
	At     time.Time `json:"at"`
	Binary string    `json:"binary"`
}

// setAsideList is the list in memory, loaded at first use.
type setAsideList struct {
	mu      sync.Mutex
	loaded  bool
	entries map[string]setAsideEntry
}

// setAsideFor holds each storage path's list: one process serves one
// storage path, tests several.
var setAsideFor sync.Map

func (mg *Manager) setAsideList() (*setAsideList, string) {
	if !cfg.HasSection("otc") {
		return nil, ""
	}
	path := filepath.Join(cfg.GetStr("otc", "storage-path"), cSetAsideFile)
	l, _ := setAsideFor.LoadOrStore(path, &setAsideList{})
	return l.(*setAsideList), path
}

// loadLocked reads the list once. l.mu held.
func (l *setAsideList) loadLocked(path string) {
	if l.loaded {
		return
	}
	l.loaded = true
	l.entries = map[string]setAsideEntry{}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err == nil {
		err = json.Unmarshal(raw, &l.entries)
	}
	if err != nil {
		log.Error("could not read the content set aside:", err)
	}
}

// saveLocked writes the list (temporary file, then rename). l.mu held.
func (l *setAsideList) saveLocked(path string) {
	if len(l.entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Error("could not update the content set aside:", err)
		}
		return
	}
	raw, err := json.Marshal(l.entries)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, raw, 0o600); err == nil { // perms: rw-------
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		log.Error("could not update the content set aside:", err)
	}
}

// isSetAside: hash is set aside - not processed by the backfill, Reprocess
// or a thumbnail fix.
func (mg *Manager) isSetAside(hash string) bool {
	l, path := mg.setAsideList()
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loadLocked(path)
	_, ok := l.entries[hash]
	return ok
}

// binaryID names the running binary, for set-aside content to be tried
// again under another one; "" when it can't be told.
var binaryID = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// setAside takes content off processing until retrySetAside gives it
// back: it was being processed at cStrikesToSetAside deaths.
func (mg *Manager) setAside(hash string) {
	// The marker is ours, but a path is built from what it holds.
	if !dao.IsContentHash(hash) {
		return
	}
	log.Error("setting", hash, "aside: the device stopped twice while processing it")
	mg.donePendingAnalysis(hash)
	if !mg.hasThumbnail(hash) {
		// The backfill's own marker (thumbnail_backfill.go) keeps it from
		// being tried at the next sign-in.
		if err := os.WriteFile(blobPath(hash)+cNoThumbnailSuffix, []byte(cDecoders), 0o600); err != nil { // perms: rw-------
			log.Error("could not mark", hash, "as set aside:", err)
		}
	}
	if l, path := mg.setAsideList(); l != nil {
		l.mu.Lock()
		l.loadLocked(path)
		l.entries[hash] = setAsideEntry{At: time.Now(), Binary: binaryID()}
		l.saveLocked(path)
		l.mu.Unlock()
	}
	file, err := mg.dao.GetFileByHash(hash)
	if err != nil {
		return // gone meanwhile
	}
	// An Alert of its own (no other error joins it, nor it them).
	title := filepath.Base(file.Path) + " was set aside"
	if _, e := mg.dao.UpsertErrorNotification("", title, "the device stopped twice while processing it. It is kept as it is, without the thumbnail, tags or faces it was missing, and tried again after the next update."); e != nil {
		log.Error("error recording the alert:", e)
	}
}

// retrySetAside gives back to processing the content set aside under
// another binary or longer than cSetAsideRetry ago: it goes back to
// pending_analysis (the first sign-in resumes it) and loses the backfill's
// marker. Called once at start, before anything is processed.
func (mg *Manager) retrySetAside() {
	l, path := mg.setAsideList()
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loadLocked(path)
	if len(l.entries) == 0 {
		return
	}
	now, bin := time.Now(), binaryID()
	retried := 0
	for hash, e := range l.entries {
		if e.Binary == bin && bin != "" && now.Sub(e.At) < cSetAsideRetry {
			continue
		}
		if err := mg.dao.AddPendingAnalysis(hash); err != nil {
			log.Error("could not give", hash, "back to processing:", err)
			continue
		}
		marker := blobPath(hash) + cNoThumbnailSuffix
		if raw, err := os.ReadFile(marker); err == nil && string(raw) == cDecoders {
			os.Remove(marker)
		}
		delete(l.entries, hash)
		retried++
	}
	if retried > 0 {
		l.saveLocked(path)
		log.Info(fmt.Sprintf("trying again %d file(s) set aside under another version or long ago", retried))
	}
}
