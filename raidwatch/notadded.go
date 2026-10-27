// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// While the mirror is degraded, scripts/raid_watch.py (root) adds a disk
// that is plugged in only when it is blank or was a member of this very
// mirror; any other - a backup drive, a card fresh from the shop with its
// filesystem, another array's disk - is left alone, and listed in
// notAddedPath (root's directory on the /run tmpfs) while it stays
// plugged in. Each one gets an Alert saying why and what to do, once per
// time it is plugged in: the ones alerted are kept in <storage>/
// .raid-not-added, and one no longer listed is forgotten, so plugging it
// in again says so again. Settings offers such a disk to be erased and
// added (mirrordisk.go), which the Alert points to - but not one something
// holds (mounted, part of another array that runs), which raid_watch.py
// marks held.
var notAddedPath = "/run/otc-raid-watch/not-added.json"

const (
	notAddedFile = ".raid-not-added"
	// What is read of the file, and kept of it: root writes it, but it is
	// shown to the owner.
	notAddedMaxBytes = 16 << 10
	notAddedMaxDisks = 8
	notAddedMaxField = 200
)

type notAddedDisk struct {
	ID   string `json:"id"`
	Disk string `json:"disk"`
	// For Settings (mirrordisk.go): what the disk calls itself, its size in
	// bytes, whether something holds it (a mount, an array: never offered),
	// and the id Settings may offer it by - absent when it may not.
	Model string `json:"model"`
	Size  int64  `json:"size"`
	Held  bool   `json:"held"`
	Offer string `json:"offer"`
}

// offered: Settings may offer the disk, to be erased and added.
func (d notAddedDisk) offered() bool {
	return reOfferID.MatchString(d.Offer) && !d.Held && d.Size > 0
}

// readNotAdded is the disks raid_watch.py left alone; ok is false when the
// file can't be read (no watcher yet, an older one that doesn't write it),
// which changes nothing.
func readNotAdded(path string) (disks []notAddedDisk, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, notAddedMaxBytes+1))
	if err != nil || len(raw) > notAddedMaxBytes {
		return nil, false
	}
	var file struct {
		Disks []notAddedDisk `json:"disks"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return nil, false
	}
	for _, d := range file.Disks {
		d.ID, d.Disk, d.Model = cleanField(d.ID), cleanField(d.Disk), cleanField(d.Model)
		if d.ID == "" || len(disks) == notAddedMaxDisks {
			continue
		}
		disks = append(disks, d)
	}
	return disks, true
}

// cleanField keeps a field to one short line of printable text.
func cleanField(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if len(s) > notAddedMaxField {
		s = strings.ToValidUTF8(s[:notAddedMaxField], "")
	}
	return s
}

// checkNotAdded raises the Alert for each disk raid_watch.py left alone
// that hasn't had one since it was plugged in.
func checkNotAdded(storagePath string, n Notify) {
	disks, ok := readNotAdded(notAddedPath)
	if !ok {
		return
	}
	alerted := loadNotAdded(storagePath)
	now := map[string]bool{}
	var fresh []notAddedDisk
	for _, d := range disks {
		now[d.ID] = true
		if !alerted[d.ID] {
			fresh = append(fresh, d)
		}
	}
	// Recorded first: a crash right after must not alert twice.
	if !sameIDs(alerted, now) {
		saveNotAdded(storagePath, now)
	}
	for _, d := range fresh {
		log.Info("raid: a disk was plugged in and left alone, not added to the mirror:", d.Disk)
		// Settings offers it, to be erased and added - unless something
		// holds it, when it isn't offered either.
		detail := i18n.DevAlertRaidNotAddedDetail(d.Disk)
		if d.Held {
			detail = i18n.DevAlertRaidNotAddedDetailInUse(d.Disk)
		}
		n.alert(alerttext.New(i18n.DevAlertRaidNotAddedTitle(), detail))
	}
}

func sameIDs(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

func loadNotAdded(storagePath string) map[string]bool {
	ids := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(storagePath, notAddedFile))
	if err != nil {
		return ids
	}
	var list []string
	if json.Unmarshal(b, &list) == nil {
		for _, id := range list {
			ids[id] = true
		}
	}
	return ids
}

// saveNotAdded records the disks alerted; a failure to record only means
// one may be said again.
func saveNotAdded(storagePath string, ids map[string]bool) {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	tmp := filepath.Join(storagePath, notAddedFile+".tmp")
	if os.WriteFile(tmp, b, 0o600) != nil || os.Rename(tmp, filepath.Join(storagePath, notAddedFile)) != nil {
		log.Error("raid: could not record the disks left out of the mirror")
		_ = os.Remove(tmp)
	}
}
