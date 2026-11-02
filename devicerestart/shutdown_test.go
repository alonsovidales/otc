// SPDX-License-Identifier: AGPL-3.0-or-later

package devicerestart

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alonsovidales/otc/devtext"
)

func shutDownSeams(t *testing.T, installed bool, up time.Duration) string {
	t.Helper()
	oldPath, oldInstalled, oldUptime := powerOffRequestPath, powerOffRunnerInstalled, uptime
	t.Cleanup(func() { powerOffRequestPath, powerOffRunnerInstalled, uptime = oldPath, oldInstalled, oldUptime })
	powerOffRequestPath = filepath.Join(t.TempDir(), "poweroff.request")
	powerOffRunnerInstalled = func() bool { return installed }
	uptime = func() (time.Duration, error) { return up, nil }
	return powerOffRequestPath
}

func TestShutDownWritesItsOwnTrigger(t *testing.T) {
	p := shutDownSeams(t, true, time.Hour)
	reboot := seams(t, true, time.Hour)
	if err := ShutDown(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("no trigger file:", err)
	}
	if _, err := os.Stat(reboot); err == nil {
		t.Fatal("a shutdown wrote the restart's trigger")
	}
}

func TestShutDownWithoutRunner(t *testing.T) {
	p := shutDownSeams(t, false, time.Hour)
	if err := ShutDown(); !errors.Is(err, ErrShutDownNotInstalled) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("trigger written without a runner")
	}
}

func TestShutDownRightAfterBootIsRefused(t *testing.T) {
	p := shutDownSeams(t, true, 30*time.Second)
	err := ShutDown()
	if err == nil {
		t.Fatal("a shutdown 30 s after boot must be refused")
	}
	if got := devtext.Text("en", err); got != "the device has only just started; try again in a minute" {
		t.Errorf("refused with %q", got)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("trigger written")
	}
}

func TestShutDownWriteFailure(t *testing.T) {
	shutDownSeams(t, true, time.Hour)
	powerOffRequestPath = filepath.Join(t.TempDir(), "missing", "poweroff.request")
	err := ShutDown()
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := devtext.Text("en", err); len(got) < 31 || got[:31] != "could not ask for the shutdown:" {
		t.Errorf("refused with %q", got)
	}
}
