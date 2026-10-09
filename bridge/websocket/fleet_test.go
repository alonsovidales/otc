// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"sync"
	"testing"

	"github.com/alonsovidales/otc/bridge/fleet"
)

// FleetStats counts devices with a live connection, their connections, the
// idle ones and the paired clients - nothing else.
func TestFleetStats(t *testing.T) {
	mg := &Manager{bridges: map[string]*bridgePool{
		"cala.off-the.cloud": {liveCount: 5, availableConns: make([]*deviceRelay, 3), lock: &sync.Mutex{}},
		"pit.off-the.cloud":  {liveCount: 2, availableConns: make([]*deviceRelay, 2), lock: &sync.Mutex{}},
		"gone.off-the.cloud": {liveCount: 0, lock: &sync.Mutex{}}, // counting down to its offline alert
	}, pairedByAddr: map[string]int{"203.0.113.7|cala.off-the.cloud": 2, "2001:db8::/64|pit.off-the.cloud": 1}}
	want := fleet.BridgeStats{Devices: 2, DeviceConns: 7, Idle: 5, Clients: 3}
	if got := mg.FleetStats(); got != want {
		t.Fatalf("%+v, want %+v", got, want)
	}
}
