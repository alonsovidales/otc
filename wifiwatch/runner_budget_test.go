// SPDX-License-Identifier: AGPL-3.0-or-later

package wifiwatch

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// The root side's restart (scripts/device-runner/otc-wifi-restart-runner.sh)
// reaches its reboot fallback within RUN_BUDGET of starting the restart,
// and that budget - with the checks before it - fits well inside its
// unit's TimeoutStartSec, which in turn fits inside the watchdog's wait for
// an answer: systemd killing the runner first left the device off its
// network with no reboot, and the status "running" for good.
func TestWifiRestartRunnerEndsInsideItsUnit(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	runner := read("../scripts/device-runner/otc-wifi-restart-runner.sh")
	number := func(name string) int {
		t.Helper()
		m := regexp.MustCompile(`(?m)^` + name + `=([0-9]+)$`).FindStringSubmatch(runner)
		if m == nil {
			t.Fatalf("the runner sets no %s", name)
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	budget, final, connect := number("RUN_BUDGET"), number("FINAL_WAIT"), number("CONNECT_WAIT")
	m := regexp.MustCompile(`(?m)^TimeoutStartSec=([0-9]+)min$`).FindStringSubmatch(read("../scripts/device-runner/otc-wifi-restart.service"))
	if m == nil {
		t.Fatal("otc-wifi-restart.service has no TimeoutStartSec in minutes")
	}
	minutes, _ := strconv.Atoi(m[1])
	unit := time.Duration(minutes) * time.Minute
	// The checks before the restart (the lock, its NetworkManager connection,
	// at most 20 s), power saving switched off after a connection back at the
	// very end of the last wait (at most 10 s past RUN_BUDGET) and the reboot
	// itself take well under a minute.
	if got := time.Duration(budget)*time.Second + time.Minute; got > unit {
		t.Errorf("RUN_BUDGET %d s plus a minute is %v, more than the unit's TimeoutStartSec %v", budget, got, unit)
	}
	if final <= 0 || final > connect || final >= budget {
		t.Errorf("FINAL_WAIT %d s must be within CONNECT_WAIT %d s and RUN_BUDGET %d s", final, connect, budget)
	}
	if unit >= DefaultConfig().RootWait {
		t.Errorf("the unit's %v is not inside the watchdog's wait for an answer, %v", unit, DefaultConfig().RootWait)
	}
	// Every step is bounded by the runner's own bounded(): no bare timeout
	// (which waits for good on a command stuck in the kernel) is left.
	if regexp.MustCompile(`(?m)^\s+timeout [0-9]`).MatchString(runner) {
		t.Error("the runner still waits on a bare timeout")
	}
}
