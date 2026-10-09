// SPDX-License-Identifier: AGPL-3.0-or-later

package devicerestart

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seams(t *testing.T, installed bool, up time.Duration) string {
	t.Helper()
	oldPath, oldInstalled, oldUptime := requestPath, runnerInstalled, uptime
	t.Cleanup(func() { requestPath, runnerInstalled, uptime = oldPath, oldInstalled, oldUptime })
	requestPath = filepath.Join(t.TempDir(), "reboot.request")
	runnerInstalled = func() bool { return installed }
	uptime = func() (time.Duration, error) { return up, nil }
	return requestPath
}

func TestRequestWritesTheTrigger(t *testing.T) {
	p := seams(t, true, time.Hour)
	if err := Request(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("no trigger file:", err)
	}
}

func TestRequestWithoutRunner(t *testing.T) {
	p := seams(t, false, time.Hour)
	if err := Request(); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("trigger written without a runner")
	}
}

func TestRequestRightAfterBootIsRefused(t *testing.T) {
	p := seams(t, true, 30*time.Second)
	if err := Request(); err == nil {
		t.Fatal("a restart 30 s after boot must be refused")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("trigger written")
	}
}

func TestReadUptime(t *testing.T) {
	if _, err := os.Stat("/proc/uptime"); err != nil {
		t.Skip("no /proc/uptime here")
	}
	up, err := readUptime()
	if err != nil || up <= 0 {
		t.Fatalf("got %v %v", up, err)
	}
}
