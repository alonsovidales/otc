// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// state.json is written on a heartbeat, not only on changes: a sync with
// nothing to say (waiting on a wrong password, at rest) still reads as
// running to a tray next to it and to `otc-sync status`.
func TestStateWrittenWhileNothingChanges(t *testing.T) {
	withConfigDir(t)
	old := stateHeartbeat
	stateHeartbeat = 20 * time.Millisecond
	t.Cleanup(func() { stateHeartbeat = old })
	eng := engine.New(&config.Config{}, "", nil)
	a := &app{quit: make(chan struct{}), eng: eng, ownsLock: true}
	go a.heartbeat()
	t.Cleanup(func() { close(a.quit) })
	var first time.Time
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		st, err := config.LoadState()
		if err != nil || st == nil {
			continue
		}
		if first.IsZero() {
			first = st.Updated

			continue
		}
		if st.Updated.After(first) {
			return // written again, with nothing changed
		}
	}
	t.Fatalf("state.json not written again (first %v)", first)
}
