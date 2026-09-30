// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"testing"
	"time"
)

// A pass that starts and ends within quietPassDelay never shows progress on
// a synced folder; a longer one does.
func TestQuietPass(t *testing.T) {
	e := &Engine{remoteStates: map[string]FolderState{"f": {Kind: StateWatching}}, held: map[string]FolderState{}, heldTimers: map[string]*time.Timer{}}
	e.setRemoteState("f", FolderState{Kind: StateScanning, Progress: 0.99})
	if got := e.remoteStates["f"].Kind; got != StateWatching {
		t.Fatalf("progress shown at once: %v", got)
	}
	e.setRemoteState("f", FolderState{Kind: StateWatching})
	time.Sleep(quietPassDelay + 200*time.Millisecond)
	if got := e.remoteStates["f"].Kind; got != StateWatching {
		t.Fatalf("a quick pass flashed: %v", got)
	}

	e.setRemoteState("f", FolderState{Kind: StateScanning, Progress: 0.5})
	time.Sleep(quietPassDelay + 200*time.Millisecond)
	e.mu.Lock()
	got := e.remoteStates["f"]
	e.mu.Unlock()
	if got.Kind != StateScanning || got.Progress != 0.5 {
		t.Fatalf("a long pass never showed: %+v", got)
	}
	e.setRemoteState("f", FolderState{Kind: StateScanning, Progress: 0.7})
	if e.remoteStates["f"].Progress != 0.7 {
		t.Fatal("progress once shown must update at once")
	}
}
