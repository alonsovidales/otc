// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func useStorageLog(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "storage.log")
	old := storageLogPath
	storageLogPath = p
	t.Cleanup(func() { storageLogPath = old })
	return p
}

func appendTo(t *testing.T, p, s string) {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

type lines []string

func (l *lines) add(s string) { *l = append(*l, s) }

// Whole lines are copied once each, a line still being written waits, and
// a restart goes on from the saved place.
func TestFollowCopiesNewLinesOnce(t *testing.T) {
	p := useStorageLog(t)
	var got lines
	var pos followPos
	if pos = followOnce(pos, got.add); len(got) != 0 || pos != (followPos{}) {
		t.Fatalf("no log yet: %v %+v", got, pos)
	}
	appendTo(t, p, "T1 [raid-watch] Monitoring /dev/md0\nT2 disks the Pi sees: sda 512.1 GB\nT3 mirror md0: cle")
	pos = followOnce(pos, got.add)
	if !slices.Equal(got, lines{"T1 [raid-watch] Monitoring /dev/md0", "T2 disks the Pi sees: sda 512.1 GB"}) {
		t.Fatalf("first look: %q", got)
	}
	appendTo(t, p, "an, degraded\n")
	pos = followOnce(pos, got.add)
	pos = followOnce(pos, got.add)
	if len(got) != 3 || got[2] != "T3 mirror md0: clean, degraded" {
		t.Fatalf("second look: %q", got)
	}

	dir := t.TempDir()
	saveFollowPos(dir, pos)
	appendTo(t, p, "T4 kernel usb 2-1: USB disconnect\n")
	got = nil
	followOnce(loadFollowPos(dir), got.add)
	if !slices.Equal(got, lines{"T4 kernel usb 2-1: USB disconnect"}) {
		t.Fatalf("after a restart: %q", got)
	}
}

// The log moved to .1: the rest of .1 first, then the new file from its
// start; a log that shrank (another file, same first line) from its start.
func TestFollowAcrossRotation(t *testing.T) {
	p := useStorageLog(t)
	var got lines
	appendTo(t, p, "T1 one\n")
	pos := followOnce(followPos{}, got.add)
	appendTo(t, p, "T2 two\n")
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	appendTo(t, p, "T3 three\n")
	pos = followOnce(pos, got.add)
	if !slices.Equal(got, lines{"T1 one", "T2 two", "T3 three"}) {
		t.Fatalf("across the move: %q", got)
	}
	if pos.Head != "T3 three" {
		t.Errorf("following %+v", pos)
	}

	if err := os.WriteFile(p, []byte("T3 three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pos.Off = 100
	got = nil
	followOnce(pos, got.add)
	if !slices.Equal(got, lines{"T3 three"}) {
		t.Fatalf("shrunk: %q", got)
	}
}

// A line longer than a whole read is handed out cut rather than holding
// the log up, and no read takes more than followChunk.
func TestFollowLongLines(t *testing.T) {
	p := useStorageLog(t)
	long := strings.Repeat("x", followChunk+10)
	appendTo(t, p, "T1 start\n"+long+"\nT2 after\n")
	var got lines
	pos := followOnce(followPos{}, got.add)
	if len(got) != 1 || got[0] != "T1 start" {
		t.Fatalf("first read: %d lines", len(got))
	}
	pos = followOnce(pos, got.add)
	if len(got) != 2 || len(got[1]) != followMaxLine+len("…") {
		t.Fatalf("the long line: %d lines, %d bytes", len(got), len(got[len(got)-1]))
	}
	for range 3 {
		pos = followOnce(pos, got.add)
	}
	if got[len(got)-1] != "T2 after" {
		t.Errorf("after it: %q", got[len(got)-1])
	}
}
