// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"log"
	"os"
	"time"
)

// A file that changes faster than it uploads - a log being written, a
// virtual machine's disk, a database - arrives at the device as bytes that
// match no hash, and was sent again, in full, at every pass. Such a "busy
// file" is left alone for a while instead, longer each time it happens
// again, and sent as usual once it holds still. As SyncModel's busyFiles.

const (
	busyFirstDelay = 2 * time.Minute
	busyMaxDelay   = time.Hour
)

type busyBackoff struct {
	until time.Time
	delay time.Duration
}

// errBusyFile: the file changed while it was being sent (or since it was
// hashed), or did so recently and is waiting (busyBackoff).
var errBusyFile = errors.New("is changing while it is sent (a busy file) - tried again later") // i18n-ignore: a file's error, only logged

// busyWait: path is backed off now.
func (e *Engine) busyWait(path string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.busy[path]

	return ok && time.Now().Before(b.until)
}

// noteBusy backs path off: twice as long as last time, from busyFirstDelay
// up to busyMaxDelay.
func (e *Engine) noteBusy(path string) {
	e.mu.Lock()
	if e.busy == nil {
		e.busy = map[string]busyBackoff{}
	}
	b := e.busy[path]
	b.delay = min(max(b.delay*2, busyFirstDelay), busyMaxDelay)
	b.until = time.Now().Add(b.delay)
	e.busy[path] = b
	e.mu.Unlock()
	log.Printf("%s changed while it was being sent - not sent again for %s", path, b.delay)
}

// noteSent: path went up whole; it starts from busyFirstDelay again.
func (e *Engine) noteSent(path string) {
	e.mu.Lock()
	delete(e.busy, path)
	e.mu.Unlock()
}

// sameStat: the file still has the size and modification time it had.
func sameStat(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
