// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/i18n"
)

// A two-way folder's sync record is only true of the folder it was made
// with. "Missing" used to mean only that nothing was at the folder's path,
// so a folder that was there but wasn't that one - the empty directory of
// a disk or network share that isn't mounted, another disk given the same
// drive letter, a folder made afresh where the synced one was - read as
// one whose every file had been deleted here, and each of them was deleted
// on the device, for good.
//
// So every two-way folder holds a small hidden file of its own,
// ".otc-sync-<folder id>", with a token the folder's record keeps too
// (folderMeta.Root). A pass goes ahead when the file holds the record's
// token. Without it - a record from before these files, a file deleted -
// the folder must still hold at least half of the files the record lists,
// unchanged, and the file is written again; a record that lists none has
// nothing to lose. Otherwise the folder waits as a missing one does,
// untouched on both sides ("Not the folder that was synced"), and carries
// on by itself once its own folder is back: the disk mounted again. One
// that is the folder to sync now - a copy restored without its hidden
// files - is removed from otc-sync and added again, which deletes nothing.
//
// The token goes by the folder, whatever the device: the check looks at
// the record as it is on disk, even one made with another device. The file
// is dot-named, so it is never synced (skip.go), and has the Hidden
// attribute on Windows. Being inside the folder, it moves and is copied
// with it, as inode numbers don't on every file system (FAT, network
// shares, a disk given another device name), which is why the identity is
// a file and not the folder's inode.

const markerPrefix = ".otc-sync-"

// markerNote is what the file says to whoever opens it.
const markerNote = "otc-sync keeps this file here to know that this is the folder it syncs with your Off The Cloud device. It is never uploaded." // i18n-ignore: written into a file, never shown by the app

// markerFile is the file's content.
type markerFile struct {
	Note  string `json:"note"`
	Token string `json:"token"`
}

// markerPath is folder id's marker file in root.
func markerPath(root, id string) string { return filepath.Join(root, markerPrefix+id) }

// readMarker is the token in folder id's marker file in root; "" when there
// is none or it can't be read.
func readMarker(root, id string) string {
	f, err := os.Open(markerPath(root, id))
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return ""
	}
	var m markerFile
	if json.Unmarshal(data, &m) != nil {
		return ""
	}

	return strings.TrimSpace(m.Token)
}

// writeMarker writes folder id's marker file in root with token.
func writeMarker(root, id, token string) error {
	data, err := json.MarshalIndent(markerFile{Note: markerNote, Token: token}, "", "  ")
	if err != nil {
		return err
	}
	p := markerPath(root, id)
	// Replaced, not written over: Windows refuses to rename a file over a
	// hidden one.
	_ = os.Remove(p)
	if err := config.WriteFileAtomic(p, append(data, '\n'), 0o644); err != nil { // perms: rw-r--r--
		return err
	}
	hideFile(p)

	return nil
}

// removeMarker deletes folder id's marker file in root, if it is there: the
// folder was removed from otc-sync. One on a disk that isn't there now
// stays, harmless.
var removeMarker = func(root, id string) { _ = os.Remove(markerPath(root, id)) }

// removeMarkers removes the markers of folders removed from otc-sync, one
// after the other - off the engine's lock, and best effort (UpdateConfig).
func removeMarkers(folders []config.RemoteFolder) {
	for _, f := range folders {
		removeMarker(f.LocalPath, f.ID)
	}
}

func newMarkerToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// rootVerdict is judgeRoot's answer.
type rootVerdict struct {
	// ok: the folder is the one the record was made with, or there is
	// nothing in the record to lose.
	ok bool
	// token is the one the record keeps from now on, and mark says the
	// marker file has to be written with it.
	token string
	mark  bool
}

// judgeRoot says whether the folder is the one its record was made with:
// recorded is the token the record keeps, marker the one in the folder's
// marker file, record the record's files and here the hashes of the files
// found here (pathKey -> hash). What is unknown here (under a directory
// that can't be read; nil: nothing) counts neither way.
func judgeRoot(recorded, marker string, record, here map[string]string, unknown func(string) bool) rootVerdict {
	if recorded != "" && marker == recorded {
		return rootVerdict{ok: true, token: recorded}
	}
	listed, unchanged := 0, 0
	for rel, entry := range record {
		if strings.HasPrefix(entry, keptOnDevicePrefix) {
			continue // deleted here, kept by the device: not expected here
		}
		if unknown != nil && unknown(rel) {
			continue
		}
		listed++
		if h, ok := here[rel]; ok && h == entry {
			unchanged++
		}
	}
	if listed > 0 && unchanged*2 < listed {
		return rootVerdict{}
	}
	token := recorded
	if token == "" {
		token = marker
	}
	if token == "" {
		token = newMarkerToken()
	}

	return rootVerdict{ok: true, token: token, mark: true}
}

// folderReplaced holds a two-way folder whose folder is there but isn't
// the one it was synced with: as a missing one, its watcher stops (it
// watches what is under the path now, not the disk mounted there later),
// the pass that finds the folder again starts a new one, and nothing
// changes on either side meanwhile.
func (e *Engine) folderReplaced(f config.RemoteFolder) {
	e.mu.Lock()
	if e.replacedRoot == nil {
		e.replacedRoot = map[string]bool{}
	}
	first := !e.replacedRoot[f.ID]
	e.replacedRoot[f.ID] = true
	if w := e.remoteWatch[f.ID]; w != nil {
		w.Stop()
		delete(e.remoteWatch, f.ID)
	}
	e.mu.Unlock()
	if first {
		log.Printf("the folder %s is not the one that was synced (a disk or share not mounted there, another disk, a folder made afresh?) - waiting for it, nothing changed on either side; if it is the one to sync now, remove it from otc-sync and add it again", f.LocalPath)
	}
	e.setRemoteState(f.ID, msgState(i18n.DeskMainFolderReplaced()))
}

// folderBack: the folder is the synced one, and says whether it wasn't at
// the pass before (folderReplaced) - then its watcher starts again.
func (e *Engine) folderBack(f config.RemoteFolder) bool {
	e.mu.Lock()
	was := e.replacedRoot[f.ID]
	delete(e.replacedRoot, f.ID)
	e.mu.Unlock()
	if was {
		log.Printf("the folder %s is the synced one again - syncing it", f.LocalPath)
	}

	return was
}
