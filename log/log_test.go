package log

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugLevel(t *testing.T) {
	test := "/tmp/levels.log"
	os.Remove(test)
	SetLogger(DEBUG, test, 10000)
	Debug("test Debug")
	Info("test Info")
	Error("test Error")

	f, err := os.Open(test)
	if err != nil {
		t.Error("the logger file:", test, "was not generated, or can't be accessed")
		t.Fail()
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	l := 0
	for scanner.Scan() {
		l++
	}

	if l != 3 {
		t.Error("expected lines: 3, but:", l, "obtained")
	}
}

func TestInfoLevel(t *testing.T) {
	test := "/tmp/levels.log"
	os.Remove(test)
	SetLogger(INFO, test, 10000)
	Debug("test Debug")
	Info("test Info")
	Error("test Error")

	f, err := os.Open(test)
	if err != nil {
		t.Error("the logger file:", test, "was not generated, or can't be accessed")
		t.Fail()
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	l := 0
	for scanner.Scan() {
		l++
	}

	if l != 2 {
		t.Error("Expected lines: 2, but:", l, "obtained")
	}
}

func TestErrorLevel(t *testing.T) {
	test := "/tmp/levels.log"
	os.Remove(test)
	SetLogger(ERROR, test, 10000)
	Debug("test Debug")
	Info("test Info")
	Error("test Error")

	f, err := os.Open(test)
	if err != nil {
		t.Error("the logger file:", test, "was not generated, or can't be accessed")
		t.Fail()
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	l := 0
	for scanner.Scan() {
		l++
	}

	if l != 1 {
		t.Error("Expected lines: 1, but:", l, "obtained")
	}
}

func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rotate.log")

	// maxSizeMB=0 means any existing content in the file makes the *next*
	// write rotate it.
	SetLogger(DEBUG, path, 0)

	Debug("first line")
	Debug("second line")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("active log file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Error("expected the active log file to contain the post-rotation line")
	}
	activeContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading active log file: %v", err)
	}
	if !strings.Contains(string(activeContent), "second line") {
		t.Error("expected the active log file to contain the post-rotation line")
	}
	if strings.Contains(string(activeContent), "first line") {
		t.Error("expected the pre-rotation line to have moved to the rotated file, not stay in the active one")
	}

	matches, err := filepath.Glob(path + "_*.old")
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one rotated .old file, got %d: %v", len(matches), matches)
	}

	oldContent, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading rotated file: %v", err)
	}
	if !strings.Contains(string(oldContent), "first line") {
		t.Error("expected the rotated file to contain the pre-rotation line")
	}
}
