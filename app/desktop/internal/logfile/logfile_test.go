// SPDX-License-Identifier: AGPL-3.0-or-later

package logfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The log starts over past maxSize, keeping the one before.
func TestRotates(t *testing.T) {
	dir := t.TempDir()
	w := &rotating{path: filepath.Join(dir, Name)}
	if err := w.open(); err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < 6; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	cur, _ := os.Stat(w.path)
	old, err := os.Stat(w.path + ".1")
	if err != nil || old.Size() != 5<<20 || cur.Size() != 1<<20 {
		t.Fatalf("current %v, previous %v (%v)", cur.Size(), old, err)
	}
}

// A standard error that fails never stops the file's line.
func TestTeeKeepsTheFile(t *testing.T) {
	var file bytes.Buffer
	if _, err := (tee{file: &file, std: failing{}}).Write([]byte("hello\n")); err != nil || file.String() != "hello\n" {
		t.Fatalf("%q %v", file.String(), err)
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, os.ErrClosed }
