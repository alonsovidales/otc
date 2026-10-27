// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"path/filepath"
	"time"

	"github.com/alonsovidales/otc/i18n"
)

// Folders' passes run side by side, a few at a time (as SyncModel's
// passSlot). One after another - as they were - a big folder's first pass
// (a 66 GB library, hours) held every other folder up, the periodic loops
// started only after all of them, and the minute's loop waited for each
// folder in turn. All at once, every folder would hash and send at the
// same time. A pass waiting for its turn says which folder it waits for.

// maxPasses is how many folders' passes run at once.
const maxPasses = 2

type passEntry struct {
	name  string
	since time.Time
}

// waitingState is a folder's state while its pass waits for name's.
func waitingState(name string) FolderState {
	m := i18n.DeskMainWaitingFor(name)

	return FolderState{
		Kind:        StateScanning,
		CurrentFile: "Waiting for " + name, // i18n-ignore: state.json's English, for the command line; Msg is the tray's
		Msg:         &m,
	}
}

// passSlot takes one of the maxPasses slots for folder id (whose folder is
// at path) and returns what frees it. While all are taken the folder shows
// waitingState, with the folder whose pass has run longest; it gives up -
// ok false - once still says the pass is no longer wanted (the folder
// removed, the device changed) or the engine stops.
func (e *Engine) passSlot(id, path string, still func() bool, show func(FolderState)) (release func(), ok bool) {
	shown := ""
	for {
		e.mu.Lock()
		if e.stopped {
			e.mu.Unlock()

			return nil, false
		}
		if e.passing == nil {
			e.passing, e.passWake = map[string]passEntry{}, make(chan struct{})
		}
		if len(e.passing) < maxPasses {
			e.passing[id] = passEntry{name: filepath.Base(path), since: time.Now()}
			e.mu.Unlock()

			return func() {
				e.mu.Lock()
				delete(e.passing, id)
				close(e.passWake)
				e.passWake = make(chan struct{})
				e.mu.Unlock()
			}, true
		}
		var first passEntry
		for _, p := range e.passing {
			if first.since.IsZero() || p.since.Before(first.since) {
				first = p
			}
		}
		wake := e.passWake
		e.mu.Unlock()
		if first.name != shown {
			shown = first.name
			show(waitingState(first.name))
		}
		select {
		case <-wake:
		case <-time.After(time.Second):
		}
		if !still() {
			return nil, false
		}
	}
}
