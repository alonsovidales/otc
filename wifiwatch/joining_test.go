// SPDX-License-Identifier: AGPL-3.0-or-later

package wifiwatch

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// A Wi-Fi change from Settings, and the minutes after it, never build a
// streak: bad minutes then are no restart. After it, the watchdog works
// as before.
func TestWifiChangeKeepsTheWatchdogQuiet(t *testing.T) {
	h := newHarness(t)
	joining := true
	h.w.joining = func() bool { return joining }

	h.run(4, pitSegs, pitRetrans, 20)
	h.run(20, pitSegs, pitRetrans, 20)
	if h.requested() || len(h.alerts) != 0 {
		t.Fatalf("restarted during a Wi-Fi change: %v", h.alerts)
	}
	if h.w.k.bad != 0 {
		t.Fatalf("a streak of %d built during the change", h.w.k.bad)
	}
	h.phase(phaseArmed)

	// Bad minutes just before the change ended don't carry over either.
	joining = false
	h.run(4, pitSegs, pitRetrans, 20)
	if h.requested() {
		t.Fatal("restarted after only 4 bad minutes")
	}
	h.run(1, pitSegs, pitRetrans, 20)
	if !h.requested() {
		t.Fatal("5 bad minutes after the change must restart as usual")
	}
}

// The restart runner found a Wi-Fi change holding the lock ("busy"): no
// restart happened, so no Alert and no giving up - the watchdog re-arms
// and restarts once the Wi-Fi is bad again.
func TestRootBusyRearms(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	h.phase(phaseRestarting)
	_ = os.Remove(h.w.requestPath)
	b, _ := json.Marshal(rootStatus{State: "failed", Message: "the Wi-Fi was being changed from Settings", Code: "busy",
		Updated: h.sim.t.UTC().Format(time.RFC3339)})
	if err := os.WriteFile(h.w.statusPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	h.run(1, 0, 0, 0)
	h.phase(phaseArmed)
	if len(h.alerts) != 0 {
		t.Fatalf("alerted: %v", h.alerts)
	}
	h.run(5, pitSegs, pitRetrans, 20)
	if !h.requested() {
		t.Fatal("no restart once bad again")
	}
}
