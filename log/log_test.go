// SPDX-License-Identifier: AGPL-3.0-or-later

package log

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #162: "info" in a config is INFO, not a silent DEBUG.
func TestParseLevelAnyCase(t *testing.T) {
	for in, want := range map[string]int{"info": INFO, "INFO": INFO, " Debug ": DEBUG, "error": ERROR} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("an unknown level was accepted")
	}
}

func TestOnlyTheNewestRotatedLogsAreKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "otc.log")
	for i := 0; i < cKeepRotated+3; i++ {
		os.WriteFile(fmt.Sprintf("%s_%d.old", path, 1700000000+i), nil, 0o600)
	}
	pruneRotated(path)
	left, _ := filepath.Glob(path + "_*.old")
	if len(left) != cKeepRotated {
		t.Fatalf("%d rotated logs left, want %d", len(left), cKeepRotated)
	}
	if !strings.HasSuffix(left[len(left)-1], fmt.Sprintf("_%d.old", 1700000000+cKeepRotated+2)) {
		t.Errorf("the newest was deleted: %v", left)
	}
}

// A value carrying a newline can't start a line of its own.
func TestOneEntryOneLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	SetLogger(INFO, path, 10)
	Info("domain:", "evil\n2026/01/01 00:00:00 INFO: forged")
	b, _ := os.ReadFile(path)
	if n := strings.Count(strings.TrimSpace(string(b)), "\n"); n != 0 {
		t.Fatalf("one entry took %d extra lines: %q", n, b)
	}
}

// A rotation that can't reopen the log (its directory gone, a read-only
// card) used to call Fatal while holding the logger's lock, which took it
// again and hung the process. It must exit instead, so systemd restarts it.
func TestFailedRotationExitsInsteadOfHanging(t *testing.T) {
	if dir := os.Getenv("OTC_LOG_ROTATE_DIR"); dir != "" {
		SetLogger(INFO, filepath.Join(dir, "x.log"), 0)
		Info("first line")
		os.RemoveAll(dir)
		Info("this one rotates into a directory that is gone")
		os.Exit(0)
	}
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFailedRotationExitsInsteadOfHanging$")
	cmd.Env = append(os.Environ(), "OTC_LOG_ROTATE_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			t.Fatalf("want exit status 1, got %v", err)
		}
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the logger hung on a failed rotation")
	}
}
