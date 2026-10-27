// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"syscall"

	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/diskspace"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// A write that would fill the storage is refused before it starts: an
// upload (UploadFile, BeginUpload), a share link's archive, a shared
// gallery's copies, a post's copy of a video. MariaDB keeps its database
// on the same disk, so a full disk also broke every insert - the alert
// that would have said so included. What is left is what the device can
// write (diskspace: the reserve for root doesn't count).

// ErrStorageFull is what such a refusal is, for errors.Is: the reply
// carries RespEnvelope.error_code "storage_full" (IsStorageFull), which
// the sync clients can take as "stop until there is room" rather than
// retry the file.
var ErrStorageFull = errors.New("not enough space on the device") // i18n-ignore: a sentinel for errors.Is; the reply shows storageFullError's text

// IsStorageFull is whether err is a write refused for want of space, or one
// that failed because the disk filled meanwhile (ENOSPC).
func IsStorageFull(err error) bool {
	return errors.Is(err, ErrStorageFull) || errors.Is(err, syscall.ENOSPC)
}

// storageFullError is checkFreeSpace's refusal: need bytes were asked for
// and left can be written, the reserve kept.
type storageFullError struct{ need, left int64 }

func (e *storageFullError) msg() i18n.Msg { return i18n.DevFilesStorageFull(e.need, e.left) }

func (e *storageFullError) Error() string { return devtext.New(e.msg()).Error() }

// Localized is the refusal in lang (devtext.Text, for the reply).
func (e *storageFullError) Localized(lang string) string {
	return devtext.New(e.msg()).Localized(lang)
}

func (e *storageFullError) Is(target error) bool { return target == ErrStorageFull }

// Every write leaves at least this much of the disk free: room for
// MariaDB, which shares the disk (InnoDB grows its files in steps of up
// to 64 MB, and sorts and temporary tables write too), the thumbnails of
// what was just saved, and the alert saying the disk is full. 1 GiB is a
// few seconds of a sync on a 4 TB mirror; on a disk under 50 GB, where it
// would be a large share, 2% of the disk instead, and never under 64 MiB.
const (
	cReserveMax = 1 << 30
	cReserveMin = 64 << 20
)

// freeSpaceReserve is the reserve for a filesystem of total bytes.
func freeSpaceReserve(total uint64) int64 {
	return min(cReserveMax, max(cReserveMin, int64(total/50)))
}

// diskUsageOf is diskspace.Of; a variable so tests can fill a disk.
var diskUsageOf = diskspace.Of

// checkFreeSpace refuses (storageFullError) a write of need bytes into dir
// that would leave less than the reserve free. When the space can't be
// read the write goes ahead: it fails by itself if the disk is full.
func checkFreeSpace(dir string, need int64) error {
	u, err := diskUsageOf(dir)
	if err != nil {
		log.Error("could not read the free space, writing anyway:", err)
		return nil
	}
	left := int64(min(u.Avail, 1<<62)) - freeSpaceReserve(u.Total)
	if need < 0 {
		need = 0
	}
	if need > left {
		log.Info("refused a write of", need>>20, "MB:", left>>20, "MB can be written before the reserve")
		return &storageFullError{need: need, left: max(left, 0)}
	}
	return nil
}

// sealedSize is what content of n bytes takes once sealed (segcrypt: a
// header and a 16-byte tag per MiB segment), rounded up.
func sealedSize(n int64) int64 {
	return n + n/(1<<16) + 4096
}
