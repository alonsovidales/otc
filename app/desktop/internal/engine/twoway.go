// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"log"
	"path"
	"sort"
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
//
// config.json is migrated the same way on disk, read again just before
// (config.Edit): saving cfg itself could put back what another process
// changed since cfg was read.
func (e *Engine) migrateFolders(cfg *config.Config) {
	if !e.migrateConfig(cfg, true) {
		return
	}
	if err := editConfigOnDisk(func(disk *config.Config) bool { return e.migrateConfig(disk, false) }); err != nil {
		log.Printf("could not save the migrated folders: %v", err)
	}
}

// migrateConfig makes cfg's folders that aren't backups two-way, and says
// whether it changed anything; logged when say.
func (e *Engine) migrateConfig(cfg *config.Config, say bool) bool {
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
		return false
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
		if say {
			log.Printf("folder %s is two-way now (%s)", f.Path, e.remotePathFor(f.Path))
		}
	}
	cfg.Folders = backups

	return true
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

// compressedPrefix marks a sync record entry for a file the device
// compressed (CompressFiles: a video at a lower resolution, a photo saved
// again smaller) and whose original's bytes it deleted: it lists the copy
// with original_removed, and original_hash the hash of the file here. The
// owner's decision (2026-11-08) is that computers keep their full-quality
// originals: the copy is the same file, never downloaded over it, and the
// original is never sent back. The entry is "compressed:<hash here>:<hash
// on the device>" and stands for those two baselines (recordBaselines), so
// while neither side changes the file nothing happens, restarts included;
// a new version here goes up and replaces the copy; the copy changed on
// the device (an edit of it) comes down; deleted on either side, the
// delete goes to the other. It is kept at the file's path here, also when
// the device saved the copy under another name (renamedOriginals). An
// older version reading the record sees a hash that matches neither side
// and takes it for a conflict: the newer version keeps the name, the other
// is kept as a copy. Same marker as SyncPaths.compressedPrefix on the Mac.
const compressedPrefix = "compressed:"

func compressedEntry(here, device string) string { return compressedPrefix + here + ":" + device }

// recordBaselines is what a sync record entry says each side had after
// the last pass: the same hash; for a file kept on the device, nothing
// here and its hash there; for a compressed one, the original here and
// the copy there.
func recordBaselines(entry string) (local, remote string) {
	if hash, kept := strings.CutPrefix(entry, keptOnDevicePrefix); kept {
		return "", hash
	}
	if pair, ok := strings.CutPrefix(entry, compressedPrefix); ok {
		here, device, ok := strings.Cut(pair, ":")
		if !ok || here == "" || device == "" {
			return "", "" // not one this version wrote: no baseline
		}

		return here, device
	}

	return entry, entry
}

// recordEntry is the entry that stands for two baselines: recordBaselines
// the other way.
func recordEntry(local, remote string) string {
	switch {
	case local == remote || remote == "":
		return local
	case local == "":
		return keptOnDevice(remote)
	}

	return compressedEntry(local, remote)
}

// plainEntry: entry is a hash (or nothing), not one of the markers.
func plainEntry(entry string) bool {
	return !strings.HasPrefix(entry, keptOnDevicePrefix) && !strings.HasPrefix(entry, compressedPrefix)
}

// compressedOriginal is, for a file the device lists as a compressed copy
// whose original it deleted, the original's hash; "" for any other.
func compressedOriginal(f *pb.File) string {
	if f == nil || !f.GetOriginalRemoved() || f.GetHash() == "" {
		return ""
	}

	return f.GetOriginalHash()
}

// baselinesFor is recordBaselines for a path the device lists as f (nil
// when it doesn't) and whose file here has the hash here ("" when there is
// none). A file kept on the device whose folder is no longer upload only
// there - lifted later from the web or a phone; the listing says so per
// file - has no baseline any more: it comes back here, which deletes
// nothing on the device, and both sides match again. Sending the old
// delete instead would delete what the lock protected.
//
// A compressed copy (compressedOriginal) of the file here, or of the one
// the record says was here, is the same file: the baselines are that
// original here and the copy there, so the compress is no change on either
// side and the pass keeps the compressed entry (recordEntry). Not once the
// record holds a compressed entry: a copy other than the recorded one is
// then a change on the device - an edit of the copy - and comes down.
func baselinesFor(entry string, f *pb.File, here string) (local, remote string) {
	if strings.HasPrefix(entry, keptOnDevicePrefix) && f != nil && !f.GetUploadOnly() {
		return "", ""
	}
	local, remote = recordBaselines(entry)
	if original := compressedOriginal(f); original != "" && plainEntry(entry) && (here == original || local == original) {
		return original, f.Hash
	}

	return local, remote
}

// stemOf is key without its extension, cut as the device cuts a name it
// renames (files_manager's splitExt): "a/IMG_1.HEIC" is "a/IMG_1"; a name
// that is all extension (".profile"), or whose extension holds a space,
// has none.
func stemOf(key string) string {
	ext := path.Ext(key)
	if ext == "" || ext == path.Base(key) || strings.Contains(ext, " ") {
		return key
	}

	return key[:len(key)-len(ext)]
}

// originalStems is the stems the name of a file the device renamed, with
// stem s, can come from: s itself, and for "<stem> (n)" (n of 2 or more:
// the device's renamedNames tries that once "<stem>" with the new
// extension is taken) that stem too.
func originalStems(s string) []string {
	if !strings.HasSuffix(s, ")") {
		return []string{s}
	}
	i := strings.LastIndex(s, " (")
	if i <= 0 {
		return []string{s}
	}
	n := s[i+2 : len(s)-1]
	if n == "" || n[0] == '0' || n == "1" || strings.Trim(n, "0123456789") != "" {
		return []string{s}
	}

	return []string{s, s[:i]}
}

// originalsByStem is, of a listing's edits and compressed copies (key ->
// the hash of the photo it was made from), each original by the stems a
// file here holding it can have (originalStems): an edit or a compress
// that renamed the photo (a HEIC become IMG_1.JPG, a PNG saved as x.jpg,
// or "x (2).jpg" next to another x.jpg) is what the IMG_1.HEIC or x.png
// here holds.
func originalsByStem(originals map[string]string) map[string][]string {
	var out map[string][]string
	for key, original := range originals {
		if original == "" {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		for _, s := range originalStems(stemOf(key)) {
			out[s] = append(out[s], original)
		}
	}

	return out
}

// renamedCopies is the device's compressed copies a two-way pass may pair
// with a file here under another name (renamedOriginals): not here, and
// with no record entry of their own. Sorted.
func renamedCopies(remote map[string]*pb.File, here, record map[string]string) []string {
	var out []string
	for key, f := range remote {
		if compressedOriginal(f) == "" {
			continue
		}
		if _, ok := here[key]; ok {
			continue
		}
		if _, ok := record[key]; ok {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)

	return out
}

// renamedStems is the stems of the files here that may be the original of
// a renamed copy (renamedCopies): the pass hashes those even when they are
// new, which renamedOriginals needs.
func renamedStems(remote map[string]*pb.File, here, record map[string]string) map[string]bool {
	var out map[string]bool
	for _, key := range renamedCopies(remote, here, record) {
		if out == nil {
			out = map[string]bool{}
		}
		for _, s := range originalStems(stemOf(key)) {
			out[s] = true
		}
	}

	return out
}

// renamedOriginals pairs the copies the device compressed and saved under
// another name - a PNG becomes x.jpg, a 48 MP HEIC a 12 MP x.JPG - with the
// file here they were made from, still under its own name: key here -> key
// of the copy on the device. This computer keeps its original as it is
// (the owner's decision), so a two-way pass takes the two for one file at
// the name here, which keeps the compressed entry, and leaves the copy's
// name alone: it isn't downloaded, the original isn't deleted here (the
// device no longer lists its name) and nothing is sent. here holds the
// files here (key -> path), hashes their hashes as far as the pass took
// them.
//
// A copy (renamedCopies) pairs with a path in its folder under a name it
// can come from (originalStems), at which the device lists nothing,
// whose record entry is the compressed entry of the copy's content - the
// pair as an earlier pass kept it -, or whose file here is the original,
// or was at the last pass (a plain entry): the best of those, then the
// first by name. A copy changed on the device since (an edit), or renamed
// or moved there, pairs no more: it comes down under its own name, and
// the original here, whose record says the device had the copy, goes to
// the trash as a file deleted there.
func renamedOriginals(remote map[string]*pb.File, here, hashes, record map[string]string) map[string]string {
	copies := renamedCopies(remote, here, record)
	if len(copies) == 0 {
		return nil
	}
	byStem := map[string][]string{}
	add := func(key string) {
		if remote[key] == nil {
			byStem[stemOf(key)] = append(byStem[stemOf(key)], key)
		}
	}
	for key := range here {
		add(key)
	}
	for key := range record {
		if _, ok := here[key]; !ok {
			add(key)
		}
	}
	// How well the file at key matches the copy f: 3 the pair as recorded,
	// 2 the original here, 1 the original at the last pass, 0 not at all.
	rank := func(key string, f *pb.File) int {
		entry := record[key]
		original := compressedOriginal(f)
		if strings.HasPrefix(entry, compressedPrefix) {
			if _, device := recordBaselines(entry); device == f.Hash {
				return 3
			}

			return 0
		}
		switch {
		case !plainEntry(entry):
			return 0
		case hashes[key] == original:
			return 2
		case entry == original:
			return 1
		}

		return 0
	}
	pairs := map[string]string{}
	for _, c := range copies {
		var candidates []string
		for _, s := range originalStems(stemOf(c)) {
			candidates = append(candidates, byStem[s]...)
		}
		sort.Strings(candidates)
		best, bestRank := "", 0
		for _, key := range candidates {
			if _, taken := pairs[key]; taken {
				continue
			}
			if r := rank(key, remote[c]); r > bestRank {
				best, bestRank = key, r
			}
		}
		if best != "" {
			pairs[best] = c
		}
	}

	return pairs
}

// replaceRenamedCopy deletes the compressed copy the device keeps, under
// another name (renamedOriginals: copyKey, listed as listed), of the file
// at localPath, right before a new version of it goes up at its name here:
// the new version replaces the copy, as it would at the same name. The
// copy goes first, so that a copy changed meanwhile (edited on a phone) or
// a failure leaves both as they are for the next pass, never the new
// version next to the old copy, which the next pass would bring down. A
// busy file is left before anything goes. In an upload-only folder the
// device keeps the copy (keptOnDevice, at its own name) and the new
// version goes up all the same.
func (e *Engine) replaceRenamedCopy(f config.RemoteFolder, localPath, copyKey string, listed *pb.File, newSynced map[string]string) error {
	if e.busyWait(localPath) {
		return errBusyFile
	}
	if here, err := presentAsNamed(f.LocalPath, copyKey); err != nil {
		return err
	} else if here {
		return errChangedHere
	}
	if err := e.deviceStillAsListed(listed.Path, listed); err != nil {
		return err
	}
	err := e.deleteRemote(listed.Path)
	if errors.Is(err, errKeptOnDevice) {
		newSynced[copyKey] = keptOnDevice(listed.Hash)

		return nil
	}

	return err
}

// compressedCopies is, of a listing, the device's compressed copies by
// their original's hash (linkCopy): the one at the first key in byte
// order, when there are several.
func compressedCopies(remote map[string]*pb.File) map[string]string {
	var out, at map[string]string
	for key, f := range remote {
		original := compressedOriginal(f)
		if original == "" {
			continue
		}
		if prev, ok := at[original]; ok && prev < key {
			continue
		}
		if out == nil {
			out, at = map[string]string{}, map[string]string{}
		}
		out[original], at[original] = f.Hash, key
	}

	return out
}

// withCompressedCopies adds to here, the hashes of the files here, those of
// the device's compressed copies of them (remote): such a copy is the file
// here (baselinesFor), so its delete on the device, the file moved here, is
// no loss for the mass-deletion guard (holdMassDeviceDeletes).
func withCompressedCopies(here map[string]bool, remote map[string]*pb.File) {
	for _, f := range remote {
		if original := compressedOriginal(f); original != "" && here[original] {
			here[f.Hash] = true
		}
	}
}
