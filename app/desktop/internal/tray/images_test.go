// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"strings"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// Issue #192: a folder's line says how it stands with Images; nothing
// while that isn't known, or when it is shown.
func TestFolderTitleSaysOutOfImages(t *testing.T) {
	base := config.FolderStatus{ID: "b1", Path: "/home/ana/Scans", State: "watching"}
	cases := map[string]string{
		"":            "⬆ Scans — Backed up",
		"shown":       "⬆ Scans — Backed up",
		"kept_out":    "⬆ Scans — Backed up · Kept out of Images",
		"by_parent":   "⬆ Scans — Backed up · Kept out of Images",
		"keeping":     "⬆ Scans — Backed up · Keeping out of Images…",
		"showing":     "⬆ Scans — Backed up · Showing in Images…",
		"unsupported": "⬆ Scans — Backed up · Your device needs an update to keep folders out of Images.",
	}
	for state, want := range cases {
		f := base
		f.OutOfImages = state
		if got := folderTitle(f); got != want {
			t.Errorf("%q: got %q, want %q", state, got, want)
		}
	}
}

// "What Do These Do?" names the folder it is about, never "here".
func TestImagesExplainWords(t *testing.T) {
	if strings.Contains(imagesExplainKinds, " here ") || strings.Contains(imagesExplainKinds, " its ") {
		t.Errorf("explanation points at no folder: %q", imagesExplainKinds)
	}
}
