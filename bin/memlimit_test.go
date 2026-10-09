// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"runtime/debug"
	"testing"

	"github.com/alonsovidales/otc/files_manager"
)

// A low-memory device gives Go 20% of the memory; GOMEMLIMIT still wins.
func TestSetMemoryLimitLowMemory(t *testing.T) {
	if os.Getenv("GOMEMLIMIT") != "" {
		t.Skip("GOMEMLIMIT is set")
	}
	before := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(before)
	p := filesmanager.MemoryProfile{Low: true, Total: 3789 << 20, Source: "auto"}
	setMemoryLimit(p)
	if got := debug.SetMemoryLimit(-1); got != p.GoMemoryLimit() || got>>20 != 757 {
		t.Errorf("limit %d MB, want 757", got>>20)
	}
	t.Setenv("GOMEMLIMIT", "1GiB")
	debug.SetMemoryLimit(before)
	setMemoryLimit(p)
	if got := debug.SetMemoryLimit(-1); got != before {
		t.Errorf("GOMEMLIMIT set, the limit changed to %d", got)
	}
}

type stopOrder []string

type markerStop struct{ o *stopOrder }

func (m markerStop) Stopped() { *m.o = append(*m.o, "marker") }

type dbStop struct{ o *stopOrder }

func (d dbStop) Stop() { *d.o = append(*d.o, "database") }

// The clean stop removes the run marker first, before anything that could
// hang or be killed by systemd's stop timeout.
func TestStopServicesRemovesTheMarkerFirst(t *testing.T) {
	var o stopOrder
	stopServices(markerStop{&o}, nil, dbStop{&o})
	if len(o) != 2 || o[0] != "marker" || o[1] != "database" {
		t.Errorf("stopped %v", o)
	}
}
