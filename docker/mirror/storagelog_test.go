// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageLogFormat(t *testing.T) {
	f := newFake(t)
	f.h.say("the mirror %s: %s", "md127", "both disks in sync")
	f.h.say("a line\nwith a newline\tand a tab")
	b, err := os.ReadFile(f.h.log.path)
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-10-26T10:00:00+0100 the mirror md127: both disks in sync\n" +
		"2026-10-26T10:00:00+0100 a line with a newline and a tab\n"
	if string(b) != want {
		t.Fatalf("log:\n%s\nwant:\n%s", b, want)
	}
	fi, err := os.Stat(f.h.log.path)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("log mode %v %v", fi.Mode(), err)
	}
	if fi, err := os.Stat(filepath.Dir(f.h.log.path)); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("log dir mode %v %v", fi.Mode(), err)
	}
	if got := f.said(); len(got) != 2 || got[0] != "the mirror md127: both disks in sync" {
		t.Fatalf("stderr %q", got)
	}
}

func TestStorageLogRotates(t *testing.T) {
	dir := t.TempDir()
	n := 0
	var errb bytes.Buffer
	l := &storageLog{path: filepath.Join(dir, "storage.log"), max: 100, stderr: &errb,
		now: func() time.Time { n++; return clock.Add(time.Duration(n) * time.Second) }}
	l.write("first line, 40 bytes or so....")   // 56 bytes with the time and the newline
	l.write("second line, which goes past 100") // would make 114: the file moves to .1 first
	old, _ := os.ReadFile(l.path + ".1")
	cur, _ := os.ReadFile(l.path)
	if string(old) != "2026-10-26T10:00:01+0100 first line, 40 bytes or so....\n" {
		t.Fatalf(".1: %q", old)
	}
	if string(cur) != "2026-10-26T10:00:02+0100 second line, which goes past 100\n" {
		t.Fatalf("new file: %q", cur)
	}
	l.write("third")
	l.write("fourth: the .1 before is replaced")
	old, _ = os.ReadFile(l.path + ".1")
	if !strings.HasPrefix(string(old), "2026-10-26T10:00:02+0100 second line") {
		t.Fatalf(".1: %q", old)
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestStorageLogNeverFollowsALink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	l := &storageLog{path: filepath.Join(dir, "storage.log"), max: storageLogMax, now: func() time.Time { return clock }, stderr: &errb}
	if err := os.Symlink(target, l.path); err != nil {
		t.Fatal(err)
	}
	l.write("one")
	l.write("two")
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Fatalf("written through the link: %q", b)
	}
	if got := strings.Count(errb.String(), "storage log not written"); got != 1 {
		t.Fatalf("said %d times: %q", got, errb.String())
	}
	_ = os.Remove(l.path)
	l.write("three")
	if b, _ := os.ReadFile(l.path); string(b) != "2026-10-26T10:00:00+0100 three\n" {
		t.Fatalf("log %q", b)
	}
}
