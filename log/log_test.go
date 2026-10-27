// SPDX-License-Identifier: AGPL-3.0-or-later

package log

import (
	"fmt"
	"os"
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
// again and hung the process; then it exited, and systemd started it again
// every few seconds for good. Now the lines go to stderr, nothing hangs or
// exits, and the file is used again once it can be opened.
func TestLostLogFileFallsBackToStderrAndComesBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	retryLogFile = 0 // tried again at every line
	defer func() { retryLogFile = cRetryLogFile }()
	logPath := filepath.Join(dir, "x.log")
	done := make(chan struct{})
	go func() {
		defer close(done)
		SetLogger(INFO, logPath, 0) // every line after the first rotates
		Info("first line")
		os.RemoveAll(dir)
		Info("this one rotates into a directory that is gone")
		Error("and this one finds no file either")
		os.Mkdir(dir, 0o700)
		Info("back in the file")
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the logger hung on a log file it could not open")
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the log file was not opened again: %v", err)
	}
	if !strings.Contains(string(b), "back in the file") || strings.Contains(string(b), "finds no file") {
		t.Fatalf("log file: %q", b)
	}
}

// A file that can't be opened at all at start (a read-only card) doesn't
// end the start either.
func TestUnopenableLogFileAtStart(t *testing.T) {
	retryLogFile = time.Hour
	defer func() { retryLogFile = cRetryLogFile }()
	SetLogger(INFO, filepath.Join(t.TempDir(), "missing", "x.log"), 10)
	Info("to stderr")
	mutex.Lock()
	defer mutex.Unlock()
	if file != nil {
		t.Fatal("a log file is open")
	}
	if retryLogFileAt.Before(time.Now().Add(time.Minute)) {
		t.Fatal("the file is tried again at every line, not later")
	}
}
