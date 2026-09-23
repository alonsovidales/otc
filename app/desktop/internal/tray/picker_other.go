// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package tray

import (
	"errors"
	"strings"

	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// pickRemoteFolder on Linux uses the desktop's own list dialog (zenity):
// double-clicking a row (its default button, "Open") drills into it, and
// the extra "Choose" button takes the folder being looked at. The Windows
// build draws its own window instead - see picker_windows.go.
func pickRemoteFolder(list func(string) ([]engine.RemoteEntry, error)) (string, bool) {
	current := "/"
	for {
		entries, err := list(current)
		if err != nil {
			_ = zenity.Error("Could not list the device's folders: "+err.Error(), zenity.Title("Off The Cloud"))

			return "", false
		}
		var items []string
		if current != "/" {
			items = append(items, "‹ Up")
		}
		for _, e := range entries {
			if e.IsDir {
				items = append(items, "📁 "+e.Name)
			}
		}
		if len(items) == 0 {
			items = []string{"(no subfolders here)"}
		}
		label := baseName(current)
		if label == "" {
			label = "/"
		}
		pick, err := zenity.List(
			"Remote folder: "+current+" — double-click a folder to open it, or choose “"+label+"” to keep it in sync with a folder on this computer.",
			items, zenity.Title("Choose a Remote Folder"), zenity.Width(460), zenity.Height(460),
			zenity.OKLabel("Open"), zenity.ExtraButton("Choose “"+label+"”"),
		)
		if errors.Is(err, zenity.ErrExtraButton) {
			return current, true
		}
		if err != nil {
			return "", false
		}
		switch {
		case pick == "" || pick == "(no subfolders here)":
			continue
		case pick == "‹ Up":
			current = parentPath(current)
		default:
			name := strings.TrimPrefix(pick, "📁 ")
			for _, e := range entries {
				if e.IsDir && e.Name == name {
					current = e.Path
				}
			}
		}
	}
}
