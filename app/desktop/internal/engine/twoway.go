// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"encoding/json"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Every synced folder is two-way: a file added or deleted on any computer
// (or on the device) reaches every other one. The only difference between
// a folder added from this computer and one picked on the device is its
// first pass - what is only here goes up, what is only there comes down -
// and with no sync record yet that first pass never deletes anything.
// Same as SyncModel.swift's migrateLocalFolders.

// migrateFolders turns the upload-only folders config.json may still list
// (older versions, `otc-sync add`, the tray's "Add Local Folder") into
// two-way folders on the same device path, keeping their ids - and with
// them their hash caches - and saves config.json when it changed anything.
//
// One-way backups (OneWay) are a choice again and stay as they are.
func (e *Engine) migrateFolders(cfg *config.Config) {
	var backups []config.Folder
	migrate := false
	for _, f := range cfg.Folders {
		if f.OneWay {
			backups = append(backups, f)
		} else {
			migrate = true
		}
	}
	if !migrate {
		return
	}
	have := map[string]bool{}
	for _, r := range cfg.RemoteFolders {
		have[r.ID] = true
	}
	for _, f := range cfg.Folders {
		if f.OneWay || have[f.ID] {
			continue
		}
		// Its requests - keep it out of Images (or show it), make it upload
		// only - go with it (the tray's "Sync a folder from this computer"
		// and `otc-sync add` add a Folder).
		cfg.RemoteFolders = append(cfg.RemoteFolders, config.RemoteFolder{
			ID: f.ID, RemotePath: e.remotePathFor(f.Path), LocalPath: f.Path, OutOfImages: f.OutOfImages, UploadOnly: f.UploadOnly,
		})
		log.Printf("folder %s is two-way now (%s)", f.Path, e.remotePathFor(f.Path))
	}
	cfg.Folders = backups
	if err := cfg.Save(); err != nil {
		log.Printf("could not save the migrated folders: %v", err)
	}
}

// keptOnDevicePrefix marks a sync record entry for a file deleted on this
// computer that the device kept: its folder there is upload only (issue
// #132), so the delete was refused ("upload_only"). The entry holds the
// device's hash after the prefix and stands for two baselines - absent
// here, that hash on the device (recordBaselines) - so while neither side
// changes the file, nothing happens: it isn't downloaded again and the
// delete isn't sent again. A new version on the device comes down, a file
// put back here goes up, both at once are a conflict like any other, gone
// from the device too the entry goes, and once the folder is no longer
// upload only the file comes back here (baselinesFor). An older version
// reading the record sees a hash that matches neither side and downloads
// the file, which is what it did before. Same marker as
// SyncPaths.keptOnDevice on the Mac.
const keptOnDevicePrefix = "kept-on-device:"

func keptOnDevice(hash string) string { return keptOnDevicePrefix + hash }

// recordBaselines is what a sync record entry says each side had after
// the last pass: the same hash, or for a file kept on the device, nothing
// here and its hash there.
func recordBaselines(entry string) (local, remote string) {
	if hash, kept := strings.CutPrefix(entry, keptOnDevicePrefix); kept {
		return "", hash
	}

	return entry, entry
}

// baselinesFor is recordBaselines for a path the device lists as f (nil
// when it doesn't). A file kept on the device whose folder is no longer
// upload only there - lifted later from the web or a phone; the listing
// says so per file - has no baseline any more: it comes back here, which
// deletes nothing on the device, and both sides match again. Sending the
// old delete instead would delete what the lock protected.
func baselinesFor(entry string, f *pb.File) (local, remote string) {
	if strings.HasPrefix(entry, keptOnDevicePrefix) && f != nil && !f.GetUploadOnly() {
		return "", ""
	}

	return recordBaselines(entry)
}

// The sync record - relative path -> hash as of the last pass - is what
// tells "deleted here" from "new over there"; kept on disk so a delete made
// while otc-sync wasn't running still reaches the device (it lived in
// memory only, so a restart brought such files back).
func syncedPath(folderID string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "synced")
	if err := os.MkdirAll(dir, 0o700); err != nil { // perms: rwx------
		return "", err
	}

	return filepath.Join(dir, folderID+".json"), nil
}

// loadSyncedLocked reads the folder's record the first time it is needed;
// e.mu must be held.
func (e *Engine) loadSyncedLocked(folderID string) {
	if _, ok := e.lastSynced[folderID]; ok {
		return
	}
	p, err := syncedPath(folderID)
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(data, &m) == nil && m != nil {
		e.lastSynced[folderID] = m
		// What is on disk now: a pass that changes nothing doesn't rewrite it.
		e.savedSynced[folderID] = maps.Clone(m)
	}
}

// saveSynced writes the folder's record after a pass that changed it.
// "Unchanged" means equal to what this process last wrote or read - not
// equal to nothing: a first pass that leaves the folder empty must still
// replace a record that lists files. And a write that failed is tried
// again next pass, never taken as done: a stale record left on disk takes
// a file put back later for one deleted on the other side.
func (e *Engine) saveSynced(folderID string, synced map[string]string) {
	e.mu.Lock()
	prev, ok := e.savedSynced[folderID]
	unchanged := ok && maps.Equal(prev, synced)
	e.mu.Unlock()
	if unchanged {
		return
	}
	p, err := syncedPath(folderID)
	if err != nil {
		log.Printf("could not save the sync record for %s: %v", folderID, err)

		return
	}
	data, err := json.Marshal(synced)
	if err != nil {
		log.Printf("could not save the sync record for %s: %v", folderID, err)

		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil { // perms: rw-------
		_ = os.Remove(tmp)
		log.Printf("could not save the sync record for %s: %v", folderID, err)

		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		log.Printf("could not save the sync record for %s: %v", folderID, err)

		return
	}
	e.mu.Lock()
	e.savedSynced[folderID] = maps.Clone(synced)
	e.mu.Unlock()
}

// dropSyncedLocked forgets a removed folder's record, on disk too.
func (e *Engine) dropSyncedLocked(folderID string) {
	delete(e.lastSynced, folderID)
	delete(e.savedSynced, folderID)
	if p, err := syncedPath(folderID); err == nil {
		_ = os.Remove(p)
	}
}
