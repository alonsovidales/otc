// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	"github.com/alonsovidales/otc/i18n"
)

// A two-way file whose transfer failed for a reason that doesn't go away
// in a minute - no room for it here, too large for this disk, the device
// refusing it - was tried again in full at every pass: a 20 GB download
// filled the disk to nothing every minute, then gave the space back; a
// 5 GB one on a FAT32 stick wrote 4 GB, failed and started again; and the
// folder said "Synced" all along. Now such a file is left alone for a
// while, longer each time (2 minutes doubling to an hour, as a busy file),
// a download is refused before it starts when it can't fit, and the pass
// ends saying how many files it couldn't sync and why. The same goes for a
// conflict whose upload failed, which made a new copy of the device's
// version at every pass.

// errNoSpace: there isn't room here for the file and a margin.
var errNoSpace = errors.New("not enough free space on this disk") // i18n-ignore: a file's error, only logged

// errTooLarge: the file is larger than this disk's file system holds.
var errTooLarge = errors.New("too large for this disk's file system (FAT32 holds files up to 4 GB)") // i18n-ignore: a file's error, only logged

// errBackedOff: the file failed recently and waits (failBackoff).
var errBackedOff = errors.New("failed recently - tried again later") // i18n-ignore: a file's error, only logged

// spaceReserve is left free on the disk by a download: never filled to the
// last byte, where the other programs' saves fail.
const spaceReserve = 256 << 20

// diskLimits is volumeLimits (a test sets the disk's room).
var diskLimits = volumeLimits

// roomFor refuses a download of size bytes into dir that the disk can't
// take (errTooLarge, errNoSpace), when the system says.
func roomFor(dir string, size int64) error {
	free, maxFile, ok := diskLimits(dir)
	if !ok || size < 0 {
		return nil
	}
	if size > maxFile {
		return errTooLarge
	}
	if uint64(size)+spaceReserve > free {
		return fmt.Errorf("%w (%d MB free, %d MB needed)", errNoSpace, free>>20, (size+spaceReserve)>>20) // i18n-ignore: a file's error, only logged
	}

	return nil
}

// failKind is why a file couldn't be synced, as the folder's note says it.
type failKind int

const (
	failOther failKind = iota
	failNoSpace
	failTooLarge
)

func failKindOf(err error) failKind {
	switch {
	case errors.Is(err, errNoSpace):
		return failNoSpace
	case errors.Is(err, errTooLarge):
		return failTooLarge
	}

	return failOther
}

// transient: an error that says nothing about the file - left for the next
// pass as it is, never backed off nor counted: the file changed (here or on
// the device) during the pass, is busy, or the connection went.
func transient(err error) bool {
	return errors.Is(err, errChangedHere) || errors.Is(err, errChangedOnDevice) || errors.Is(err, errBusyFile) || wsclient.Dropped(err)
}

type failBackoff struct {
	until time.Time
	delay time.Duration
	kind  failKind
}

// failWait: path failed recently and waits now; with why it failed.
func (e *Engine) failWait(path string) (failKind, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.failing[path]

	return b.kind, ok && time.Now().Before(b.until)
}

// noteFailed backs path off: twice as long as last time, from
// busyFirstDelay up to busyMaxDelay.
func (e *Engine) noteFailed(path string, err error) {
	e.mu.Lock()
	if e.failing == nil {
		e.failing = map[string]failBackoff{}
	}
	b := e.failing[path]
	b.delay = min(max(b.delay*2, busyFirstDelay), busyMaxDelay)
	b.until = time.Now().Add(b.delay)
	b.kind = failKindOf(err)
	e.failing[path] = b
	e.mu.Unlock()
	log.Printf("%s could not be synced (%v) - tried again in %s", path, err, b.delay)
}

// noteSynced: path went through; it starts from busyFirstDelay again.
func (e *Engine) noteSynced(path string) {
	e.mu.Lock()
	delete(e.failing, path)
	e.mu.Unlock()
}

// forgetFailedUnder drops the backoffs of files in root that the pass had
// nothing to do for (keep: the paths it acted on, or held back).
func (e *Engine) forgetFailedUnder(root string, keep map[string]bool) {
	prefix := filepath.Clean(root) + string(filepath.Separator)
	e.mu.Lock()
	defer e.mu.Unlock()
	for p := range e.failing {
		if strings.HasPrefix(p, prefix) && !keep[p] {
			delete(e.failing, p)
		}
	}
}

// passFailures counts the files a pass couldn't sync, by why.
type passFailures struct{ other, noSpace, tooLarge int }

func (pf *passFailures) add(k failKind) {
	switch k {
	case failNoSpace:
		pf.noSpace++
	case failTooLarge:
		pf.tooLarge++
	default:
		pf.other++
	}
}

func (pf passFailures) total() int { return pf.other + pf.noSpace + pf.tooLarge }

// note is the folder's state for them: no room first, then too large, then
// the rest; Kind "" when there are none.
func (pf passFailures) note() FolderState {
	switch n := pf.total(); {
	case n == 0:
		return FolderState{}
	case pf.noSpace > 0:
		return noteState(fmt.Sprintf("Not enough space here for %d file(s) from the device", pf.noSpace), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
			i18n.DeskMainNoSpace(pf.noSpace))
	case pf.tooLarge > 0:
		return noteState(fmt.Sprintf("%d file(s) are too large for this disk (FAT32 holds files up to 4 GB)", pf.tooLarge), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
			i18n.DeskMainTooLargeForDisk(pf.tooLarge))
	default:
		return noteState(fmt.Sprintf("%d file(s) could not be synced - see the log", n), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
			i18n.DeskMainFilesFailed(n))
	}
}
