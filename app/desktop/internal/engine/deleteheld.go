// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sort"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// "Delete Them on the Device Too…": the way out of the device-side
// mass-deletion guard (holdMassDeviceDeletes) from this computer. The
// guard holds the deletes of files gone from here at once, and the folder
// says so until they are back here or deleted on the device too - which
// was only ever done from the web or a phone. Now the folder's menu (the
// tray's submenu, `otc-sync delete-held`) asks the owner, and an answer of
// yes lets those deletes go - those exactly, never whatever is gone by the
// time the next pass looks.
//
// So the engine publishes, with the held count, a key for the held set
// (heldDeletesKey: its paths and the device's hashes of them), and the
// approval is that key, recorded in config.json with the folder
// (config.RemoteFolder.DeleteHeld) like a folder's other requests - the
// tray may be another process than the engine. The next pass compares it
// with the key of the set the guard would hold now: the same, the guard
// lets them through (every check after it still applies: the device's pin
// unconfirmed holds them as it would any delete, and keeps the approval
// for the pass that lets them go) and the approval is cleared,
// compare-and-clear; another set (a file more, one less, one changed on
// the device), it is cleared and the hold stays, so the folder shows the
// new count to be asked again. As SyncModel's deleteHeldOnDevice and
// settleDeleteApproval on the Mac, which keeps the approval in memory.

// heldDeletesKey is the key of the set of deletes the guard holds: the
// SHA-256, in hex, of each held path and the device's hash of it (the
// delete action's), one line each, sorted. "" when none is held. As
// SyncPaths.heldDeletesKey.
func heldDeletesKey(actions []action, held map[string]bool) string {
	if len(held) == 0 {
		return ""
	}
	lines := make([]string, 0, len(held))
	for _, a := range actions {
		if a.kind == actDeleteRemote && held[a.relative] {
			lines = append(lines, a.relative+"\x00"+a.hash+"\n")
		}
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// heldSet is what the guard held at a folder's last pass that got that far
// (noteMassHeld): how many deletes, and their set's key.
type heldSet struct {
	n   int
	key string
}

// approvalVerdict is what a pass makes of the owner's approval.
type approvalVerdict int

const (
	// approvalNone: nothing was asked.
	approvalNone approvalVerdict = iota
	// approvalGo: the set held now is the one approved - its deletes go.
	approvalGo
	// approvalStale: what was approved isn't what the guard holds now (or
	// it holds nothing, or this approval was acted on already) - it is
	// dropped, and the hold stays as it is.
	approvalStale
)

// judgeDeleteApproval is the verdict on approved (the key in config.json,
// "" for none) against key (the set the guard holds now, "" for none).
// done is an approval acted on whose clear couldn't be written to
// config.json (dropDeleteApproval): read back from there, it is never
// used twice.
func judgeDeleteApproval(approved, done, key string) approvalVerdict {
	switch {
	case approved == "":
		return approvalNone
	case approved == key && approved != done:
		return approvalGo
	default:
		return approvalStale
	}
}

// deleteApprovalLocked is folder id's approval in config.json and the one
// acted on that config.json still holds (approvalDone). e.mu must be held.
func (e *Engine) deleteApprovalLocked(id string) (approved, done string) {
	if e.cfg == nil {
		return "", ""
	}

	return e.cfg.DeleteHeldRequest(id), e.approvalDone[id]
}

// settleDeleteApproval acts on the verdict once the pass has planned: an
// approval used or stale is cleared (dropDeleteApproval) and logged once.
// n is how many deletes it let go, or, stale, how many the guard holds
// now. One used whose deletes wait for the device's pin to be confirmed
// (uncertain: they all do) stays: it is used by the pass that lets them
// go, as long as the set is still the same then.
func (e *Engine) settleDeleteApproval(f config.RemoteFolder, approved string, verdict approvalVerdict, n int, uncertain bool) {
	if verdict == approvalNone || (verdict == approvalGo && uncertain) {
		return
	}
	e.mu.Lock()
	said := e.approvalDone[f.ID] == approved // read back after a clear that failed
	e.mu.Unlock()
	switch {
	case said:
	case verdict == approvalGo:
		log.Printf("%s: deleting on the device too the %d files deleted here at once, as asked", f.RemotePath, n)
	case n > 0:
		log.Printf("%s: the files deleted here at once have changed since deleting them on the device too was asked - nothing deleted there; %d held now, to be asked about again", f.RemotePath, n)
	default:
		log.Printf("%s: deleting the files held on the device too was asked, but none is held now - nothing deleted there", f.RemotePath)
	}
	e.dropDeleteApproval(f.ID, approved)
}

// dropDeleteApproval clears folder id's approval once acted on - in
// config.json first, read again just before so whatever the tray or the
// command line wrote meanwhile stays, then in memory - each only while it
// still holds that key (config.ClearDeleteHeldRequest). When config.json
// can't be written the approval stays there, and a reload would bring it
// back: approvalDone keeps it from being used again (and logged again)
// until a clear is written, at a later pass.
func (e *Engine) dropDeleteApproval(id, key string) {
	err := editConfigOnDisk(func(cfg *config.Config) bool { return cfg.ClearDeleteHeldRequest(id, key) })
	e.mu.Lock()
	if e.cfg != nil {
		e.cfg.ClearDeleteHeldRequest(id, key)
	}
	first := false
	if err != nil {
		if e.approvalDone == nil {
			e.approvalDone = map[string]string{}
		}
		first = e.approvalDone[id] != key
		e.approvalDone[id] = key
	} else if e.approvalDone[id] == key {
		delete(e.approvalDone, id)
	}
	e.mu.Unlock()
	if first {
		log.Printf("could not record that folder %s's request to delete the held files on the device too was acted on (tried again later): %v", id, err)
	}
	e.notify()
}

// changedApprovalsLocked lists the two-way folders cfg brings a new
// approval for, compared with old: their pass runs at once (passSoon),
// not at the minute's poll. A folder new to the config is not one of them.
// e.mu must be held.
func (e *Engine) changedApprovalsLocked(old, cfg *config.Config) []config.RemoteFolder {
	before := map[string]string{}
	existed := map[string]bool{}
	for _, f := range old.RemoteFolders {
		before[f.ID], existed[f.ID] = f.DeleteHeld, true
	}
	var out []config.RemoteFolder
	for _, f := range cfg.RemoteFolders {
		if existed[f.ID] && f.DeleteHeld != "" && f.DeleteHeld != before[f.ID] {
			out = append(out, f)
		}
	}

	return out
}

// passSoon runs folder f's pass now - or, when one is under way (it may
// have looked at the guard already), once that one is over
// (reconcileRemoteFolder's passAgain).
func (e *Engine) passSoon(f config.RemoteFolder) {
	e.mu.Lock()
	if e.passAgain == nil {
		e.passAgain = map[string]bool{}
	}
	e.passAgain[f.ID] = true
	e.mu.Unlock()
	go e.reconcileRemoteFolder(f)
}

// remoteFolderLocked is two-way folder id as configured now. e.mu must be
// held.
func (e *Engine) remoteFolderLocked(id string) (config.RemoteFolder, bool) {
	for _, f := range e.cfg.RemoteFolders {
		if f.ID == id {
			return f, true
		}
	}

	return config.RemoteFolder{}, false
}

// deletesHeldStatusLocked fills in what the guard held at the folder's
// last pass, for the tray's "Delete Them on the Device Too…" and the
// command line. e.mu must be held.
func (e *Engine) deletesHeldStatusLocked(fs *config.FolderStatus) {
	if h, ok := e.massHeld[fs.ID]; ok {
		fs.DeletesHeld, fs.DeletesHeldKey = h.n, h.key
	}
}
