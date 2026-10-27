// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// The sync record - relative path -> hash as of the last pass - is what
// tells "deleted here" from "new over there"; kept on disk so a delete made
// while otc-sync wasn't running still reaches the device (it lived in
// memory only, so a restart brought such files back).
//
// It also says which device it was made with (recordDevice). A record is
// only true of that device: one kept across a change of device - the
// device's address changed without Disconnect, the same bridge name given
// to a device set up again from scratch - took every file the new device
// doesn't have for one deleted there, and the first pass deleted them
// here. A record of another device counts as no record at all: that pass
// is a first pass, which only copies what one side lacks and deletes
// nothing. Same file on the Mac (SyncModel's SyncRecord).
//
//	{"v": 2, "device": {"domain": "cala.off-the.cloud", "pin": "<hex>"},
//	 "files": {"a/b.txt": "<sha-256>", ...}}
//
// The file of an older version is the bare map; it is taken as the
// configured device's, which is what it was until now. An older version
// reading this one can't, and starts over with no record - again nothing
// deleted.
const recordVersion = 2

// recordDevice is the device a record was made with.
type recordDevice struct {
	// Domain is the configured address, as deviceDomainKey writes it.
	Domain string `json:"domain"`
	// Pin is the hex SHA-256 of the device's home-network certificate
	// (issue #190): made once and kept on the device's storage, so it names
	// that device - that user's - whatever address reaches it, and a device
	// set up again with empty disks gets a new one. "" when the device
	// gave none (a release before #190, no home network).
	Pin string `json:"pin,omitempty"`
}

// matches: a record made with r is a record of the device d. By the pin
// when both have one - the same device reached through another address
// keeps its record - else by the address.
func (r recordDevice) matches(d recordDevice) bool {
	if r.Pin != "" && d.Pin != "" {
		return r.Pin == d.Pin
	}

	return r.Domain == d.Domain
}

// deviceDomainKey is a configured address as records compare it: a bridge
// name in any of its spellings ("Cala.off-the.cloud",
// "wss://cala.off-the.cloud/ws") is its bare host; anything else is the
// trimmed address in lower case.
func deviceDomainKey(domain string) string {
	d := strings.TrimSpace(domain)
	if name := config.BridgeName(d); name != "" {
		return config.BridgeDomainForName(name)
	}

	return strings.TrimSuffix(strings.ToLower(d), "/")
}

// SameDevice: the two configured addresses reach the same device as far
// as the address says (a bridge name and its URL, case, a trailing
// slash). The command line and the tray refuse a change of device while
// folders are synced (they would sync with the other one), not a respelling.
func SameDevice(a, b string) bool { return deviceDomainKey(a) == deviceDomainKey(b) }

// deviceFor is the identity of the device a pass at domain talks to now.
func (e *Engine) deviceFor(domain string) recordDevice {
	d := recordDevice{Domain: deviceDomainKey(domain)}
	if pin := e.ws.LocalPin(); len(pin) > 0 {
		d.Pin = hex.EncodeToString(pin)
	}

	return d
}

// syncRecord is the record file as it is written.
type syncRecord struct {
	V      int               `json:"v"`
	Device recordDevice      `json:"device"`
	Files  map[string]string `json:"files"`
}

// syncedFile is what loadSyncedLocked keeps of a folder's file: the device
// it was made with (nil for an older version's file, which has none).
type syncedFile struct {
	device *recordDevice
}

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

// parseRecord reads a record file: this version's, or an older version's
// bare map (device nil). Paths are keyed by their NFC form (pathKey): two
// entries that differ only in normalization keep the one already in NFC.
func parseRecord(data []byte) (files map[string]string, device *recordDevice, ok bool) {
	var rec syncRecord
	if json.Unmarshal(data, &rec) == nil && rec.V == recordVersion && rec.Files != nil {
		d := rec.Device

		return nfcKeys(rec.Files), &d, true
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil || m == nil {
		return nil, nil, false
	}

	return nfcKeys(m), nil, true
}

// nfcKeys is m keyed by pathKey.
func nfcKeys(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		key := pathKey(k)
		if _, dup := out[key]; dup && key != k {
			continue // the NFC spelling, when both are there, wins
		}
		out[key] = v
	}

	return out
}

// pathKey is a relative path as the two-way passes compare it: its NFC
// form (the Mac's SyncPaths.key). The device keeps paths byte for byte, as
// clients sent them - a name typed on a phone is NFC, the Mac's file
// system hands out NFD - so the same name could be two entries there, and
// two keys here. Sent paths of new files are NFC; a file the device already
// has is always addressed by the device's own bytes, never renamed to NFC.
func pathKey(rel string) string { return norm.NFC.String(rel) }

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
	files, device, ok := parseRecord(data)
	if !ok {
		log.Printf("the sync record of %s can't be read - taken as none (nothing is deleted)", folderID)

		return
	}
	e.lastSynced[folderID] = files
	e.syncedFiles[folderID] = syncedFile{device: device}
	// What is on disk now: a pass that changes nothing doesn't rewrite it.
	// An older version's file is rewritten once, with its device.
	if device != nil {
		e.savedSynced[folderID] = maps.Clone(files)
		e.savedDevice[folderID] = *device
	}
}

// recordFor is folder id's record for the device a pass talks to (dev):
// the one on disk, or none when it was made with another device - then
// the pass deletes nothing, on either side. An older version's record,
// which doesn't say, is taken as this device's. e.mu must be held.
func (e *Engine) recordForLocked(folderID string, dev recordDevice) map[string]string {
	e.loadSyncedLocked(folderID)
	last := e.lastSynced[folderID]
	if sf, ok := e.syncedFiles[folderID]; ok && sf.device != nil && !sf.device.matches(dev) && len(last) > 0 {
		log.Printf("the sync record of %s was made with another device (%s) - this pass only copies what one side lacks, and deletes nothing", folderID, sf.device.Domain)

		return map[string]string{}
	}

	return last
}

// saveSynced writes the folder's record for the device configured now; a
// pass uses saveSyncedFor with the device it started on.
func (e *Engine) saveSynced(folderID string, synced map[string]string) {
	e.mu.Lock()
	domain := e.cfg.Domain
	e.mu.Unlock()
	e.saveSyncedFor(folderID, e.deviceFor(domain), synced)
}

// saveSyncedFor writes the folder's record, made with dev, after a pass
// that changed it. "Unchanged" means equal to what this process last wrote
// or read - not equal to nothing: a first pass that leaves the folder empty
// must still replace a record that lists files. And a write that failed is
// tried again next pass, never taken as done: a stale record left on disk
// takes a file put back later for one deleted on the other side.
func (e *Engine) saveSyncedFor(folderID string, dev recordDevice, synced map[string]string) {
	e.mu.Lock()
	prev, ok := e.savedSynced[folderID]
	unchanged := ok && maps.Equal(prev, synced) && e.savedDevice[folderID] == dev
	e.mu.Unlock()
	if unchanged {
		return
	}
	p, err := syncedPath(folderID)
	if err != nil {
		log.Printf("could not save the sync record for %s: %v", folderID, err)

		return
	}
	data, err := json.Marshal(syncRecord{V: recordVersion, Device: dev, Files: synced})
	if err != nil {
		log.Printf("could not save the sync record for %s: %v", folderID, err)

		return
	}
	if err := config.WriteFileAtomic(p, data, 0o600); err != nil {
		log.Printf("could not save the sync record for %s: %v", folderID, err)

		return
	}
	e.mu.Lock()
	e.savedSynced[folderID] = maps.Clone(synced)
	e.savedDevice[folderID] = dev
	e.syncedFiles[folderID] = syncedFile{device: &dev}
	e.mu.Unlock()
}

// dropSyncedLocked forgets a removed folder's record, on disk too.
func (e *Engine) dropSyncedLocked(folderID string) {
	delete(e.lastSynced, folderID)
	delete(e.savedSynced, folderID)
	delete(e.savedDevice, folderID)
	delete(e.syncedFiles, folderID)
	if p, err := syncedPath(folderID); err == nil {
		_ = os.Remove(p)
	}
}
