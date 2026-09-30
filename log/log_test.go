// SPDX-License-Identifier: AGPL-3.0-or-later

package log

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
