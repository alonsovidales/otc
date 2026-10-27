// SPDX-License-Identifier: AGPL-3.0-or-later

// Package storagewatch tells the owner when the device's storage is filling
// up, before it is full: an Alert once it is 90% full and another at 98%,
// which is also pushed. A full disk stops more than uploads - MariaDB keeps
// its database on it, so every insert failed, the alert that would have
// said so included - and the device refuses a write that would leave less
// than its reserve (files_manager's freespace.go), so the alert comes
// while there is still room to act. Machine-level: the main instance only
// (websocket/machine_alerts.go), like the mirror's disks.
package storagewatch

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/diskspace"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/push"
)

// The levels alerted, in percent full (diskspace.Usage.Percent: of the
// space the device can use), and how far under one the storage must go
// before that level can be alerted again - a disk hovering at 90% isn't
// alerted at every look.
const (
	cAlmostFull = 90
	cFull       = 98
	cRearm      = 5
)

const (
	firstCheck = time.Minute
	checkEvery = 5 * time.Minute
	// stateFile keeps, in the storage folder, the level last alerted, so a
	// restart doesn't alert it again.
	stateFile = ".storage-alerted"
)

// Notify is where the alert and the push go: dao.AddStorageNotification
// (an Alert row of its own) and Push.NotifyText.
type Notify struct {
	AlertText func(a alerttext.Alert)
	PushText  func(t push.Text)
}

// usageOf is diskspace.Of; a variable for tests.
var usageOf = diskspace.Of

// Watch looks at the filesystem holding storagePath every checkEvery, for
// good.
func Watch(storagePath string, n Notify) {
	time.Sleep(firstCheck)
	for {
		check(storagePath, n)
		time.Sleep(checkEvery)
	}
}

// level is the highest level pct is at: 0, cAlmostFull or cFull.
func level(pct int) int {
	switch {
	case pct >= cFull:
		return cFull
	case pct >= cAlmostFull:
		return cAlmostFull
	}
	return 0
}

// rearmed is alerted, the level last alerted, lowered while pct is more
// than cRearm under it: from cFull to cAlmostFull, from there to none.
func rearmed(alerted, pct int) int {
	for alerted > 0 && pct < alerted-cRearm {
		if alerted == cFull {
			alerted = cAlmostFull
		} else {
			alerted = 0
		}
	}
	return alerted
}

// check alerts a level the storage has reached since the last one alerted.
func check(storagePath string, n Notify) {
	u, err := usageOf(storagePath)
	if err != nil {
		log.Error("disk space: could not read how full the storage is:", err)
		return
	}
	pct := u.Percent()
	prev := loadAlerted(storagePath)
	alerted := rearmed(prev, pct)
	if now := level(pct); now > alerted {
		title := i18n.DevAlertStorageAlmostFull(pct)
		if now == cFull {
			title = i18n.DevAlertStorageFull(pct)
		}
		log.Info("disk space:", pct, "% full,", u.Avail>>20, "MB left: alerted")
		if n.AlertText != nil {
			n.AlertText(alerttext.New(title, i18n.DevAlertStorageDetail(int64(u.Avail), int64(u.Size()))))
		}
		if now == cFull && n.PushText != nil {
			n.PushText(push.StorageFull())
		}
		alerted = now
	}
	if alerted != prev {
		saveAlerted(storagePath, alerted)
	}
}

func loadAlerted(storagePath string) int {
	b, err := os.ReadFile(filepath.Join(storagePath, stateFile))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return level(n)
}

// saveAlerted records the level alerted. A storage too full to take even
// this means the level is alerted again at the next start.
func saveAlerted(storagePath string, alerted int) {
	path := filepath.Join(storagePath, stateFile)
	if alerted == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Error("disk space: could not forget the level alerted:", err)
		}
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(alerted)+"\n"), 0o600); err != nil { // perms: rw-------
		log.Error("disk space: could not record the level alerted:", err)
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Error("disk space: could not record the level alerted:", err)
		os.Remove(tmp)
	}
}
