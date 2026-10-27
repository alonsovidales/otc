// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// The local hash cache on disk: <config dir>/hashes/<folder id>.json, one
// entry per file with the size and modification time the hash was computed
// for. It is what makes the checking pass after a restart as fast as the
// ones before it - without it every start re-reads the whole folder (66 GB
// on the reference Mac) just to confirm nothing changed. Same file shape as
// the macOS app's, per folder so a removed folder takes its cache with it.
// Losing the file is harmless: the next pass rebuilds it.

type hashCacheFile struct {
	Size    int64  `json:"size"`
	ModNano int64  `json:"mod_nano"`
	Hash    string `json:"hash"`
	// CtimeNano is the change time the hash was taken at (changeTime;
	// absent where it isn't read, and in an older version's file).
	CtimeNano int64 `json:"ctime_nano,omitempty"`
}

// hashCacheSaveEvery: a pass that hashes for longer than this writes the
// cache as it goes, so an otc-sync stopped (or killed) during a long first
// pass doesn't read everything again at the next start.
const hashCacheSaveEvery = 5 * time.Minute

func hashCachePath(folderID string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "hashes")
	if err := os.MkdirAll(dir, 0o700); err != nil { // perms: rwx------
		return "", err
	}

	return filepath.Join(dir, folderID+".json"), nil
}

// loadHashCacheLocked reads the folder's cache the first time the folder is
// checked in this process; e.mu must be held.
func (e *Engine) loadHashCacheLocked(folderID string) {
	if e.hashLoaded[folderID] {
		return
	}
	e.hashLoaded[folderID] = true
	p, err := hashCachePath(folderID)
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var stored map[string]hashCacheFile
	if json.Unmarshal(data, &stored) != nil {
		return
	}
	m := make(map[string]hashEntry, len(stored))
	for path, v := range stored {
		m[path] = hashEntry{size: v.Size, modTime: time.Unix(0, v.ModNano), ctime: v.CtimeNano, hash: v.Hash}
	}
	e.hashCache[folderID] = m
}

// saveHashCache writes the folder's cache if it changed: at the end of
// every pass, every hashCacheSaveEvery during a long one
// (saveHashCacheEvery), and when the engine stops (saveAllHashCaches). The
// dirty check comes first: most passes change nothing, and copying a large
// cache under e.mu for nothing held up everything else that needs the
// lock.
func (e *Engine) saveHashCache(folderID string) {
	e.mu.Lock()
	if !e.hashDirty[folderID] {
		e.mu.Unlock()

		return
	}
	delete(e.hashDirty, folderID)
	if e.hashSavedAt == nil {
		e.hashSavedAt = map[string]time.Time{}
	}
	e.hashSavedAt[folderID] = time.Now()
	stored := make(map[string]hashCacheFile, len(e.hashCache[folderID]))
	for path, v := range e.hashCache[folderID] {
		stored[path] = hashCacheFile{Size: v.size, ModNano: v.modTime.UnixNano(), CtimeNano: v.ctime, Hash: v.hash}
	}
	e.mu.Unlock()
	p, err := hashCachePath(folderID)
	if err != nil {
		return
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return
	}
	if err := config.WriteFileAtomic(p, data, 0o600); err != nil {
		// Still to be written: the next save tries again.
		e.mu.Lock()
		e.hashDirty[folderID] = true
		e.mu.Unlock()
	}
}

// saveHashCacheEvery saves the folder's cache during a pass, at most every
// hashCacheSaveEvery (the clock starts at its first call).
func (e *Engine) saveHashCacheEvery(folderID string) {
	e.mu.Lock()
	if e.hashSavedAt == nil {
		e.hashSavedAt = map[string]time.Time{}
	}
	at, seen := e.hashSavedAt[folderID]
	if !seen {
		e.hashSavedAt[folderID] = time.Now()
	}
	due := seen && e.hashDirty[folderID] && time.Since(at) >= hashCacheSaveEvery
	e.mu.Unlock()
	if due {
		e.saveHashCache(folderID)
	}
}

// saveAllHashCaches writes every changed cache: the engine is stopping
// (the tray's Quit, the service stopped) - possibly in the middle of a
// pass, whose hashing would otherwise be lost.
func (e *Engine) saveAllHashCaches() {
	e.mu.Lock()
	var ids []string
	for id := range e.hashDirty {
		if e.backupConfiguredLocked(id) || e.remoteConfiguredLocked(id) {
			ids = append(ids, id)
		}
	}
	e.mu.Unlock()
	for _, id := range ids {
		e.saveHashCache(id)
	}
}

// pruneHashCache drops the entries of files that are no longer in the
// folder, so renames and deletes don't grow the cache (and its file)
// forever. local and failed are enumerateFiles(root) of this pass, taken
// before anything is hashed: entries added later in the pass stay. What
// is under a directory that could not be read is unknown, not gone, so
// its entries stay too - a folder with one such directory for good
// (lost+found at a mount's root) is still pruned everywhere else.
func (e *Engine) pruneHashCache(folderID, root string, local, failed []string) {
	keep := make(map[string]struct{}, len(local))
	for _, p := range local {
		keep[p] = struct{}{}
	}
	unknown := make([]string, 0, len(failed))
	for _, d := range failed {
		unknown = append(unknown, filepath.Join(root, filepath.FromSlash(d))+string(filepath.Separator))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// Loaded first: pruning a cache not read yet would do nothing, and
	// reading it later would bring every stale entry back.
	e.loadHashCacheLocked(folderID)
	for p := range e.hashCache[folderID] {
		if _, ok := keep[p]; ok || underAny(p, unknown) {
			continue
		}
		delete(e.hashCache[folderID], p)
		e.hashDirty[folderID] = true
	}
}

// underAny: p is inside one of dirs (each ending in a separator).
func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d) {
			return true
		}
	}

	return false
}

// dropHashCacheLocked forgets a removed folder's cache, on disk too.
func (e *Engine) dropHashCacheLocked(folderID string) {
	delete(e.hashCache, folderID)
	delete(e.hashDirty, folderID)
	delete(e.hashLoaded, folderID)
	if p, err := hashCachePath(folderID); err == nil {
		_ = os.Remove(p)
	}
}
