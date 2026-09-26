// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// Issue #138: while hashing, the CLI names the file being checked instead
// of a bare "checking…"; during a transfer it keeps the percentage.
func TestDescribe(t *testing.T) {
	cases := []struct {
		st   config.FolderStatus
		want string
	}{
		{config.FolderStatus{State: "scanning"}, "checking…"},
		{config.FolderStatus{State: "scanning", CurrentFile: "Checking 12/400 · IMG_0012.jpg"}, "Checking 12/400 · IMG_0012.jpg"},
		{config.FolderStatus{State: "scanning", Progress: 0.5, CurrentFile: "IMG_0012.jpg"}, "50% IMG_0012.jpg"},
		{config.FolderStatus{State: "watching"}, "synced"},
		{config.FolderStatus{State: "error", Error: "boom"}, "error: boom"},
	}
	for _, c := range cases {
		if got := Describe(c.st); got != c.want {
			t.Errorf("Describe(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
}
