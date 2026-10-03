// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"os"
	"path/filepath"
	"testing"
)

// A piece of the log ends on a whole line, and the next one starts there.
func TestReadLogRangeWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otc.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthr"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := readLogRange(path, 0, 11, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if l.Text != "one\ntwo\n" || l.NextOffset != 8 {
		t.Fatalf("got %q next %d, want the two whole lines", l.Text, l.NextOffset)
	}
	l, err = readLogRange(path, 4, 11, 4)
	if err != nil {
		t.Fatal(err)
	}
	if l.Text != "two\n" || l.NextOffset != 8 {
		t.Fatalf("from 4: got %q next %d", l.Text, l.NextOffset)
	}
}
