// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package tray

import (
	"errors"
	"slices"

	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/engine"
	"github.com/alonsovidales/otc/i18n"
)

// pickRemoteFolder on Linux uses the desktop's own list dialog (zenity):
// double-clicking a row (its default button, "Open") drills into it, and
// the extra "Choose" button takes the folder being looked at. The Windows
// build draws its own window instead - see picker_windows.go. Its words
// are in the menu's language, and a row picked maps back by its index.
func pickRemoteFolder(list func(string) ([]engine.RemoteEntry, error)) (string, bool) {
	current := "/"
	for {
		l := lang()
		entries, err := list(current)
		if err != nil {
			_ = dlgError(i18n.DeskFoldersPickerListFailed(errorText(l, err)).Render(l), dlgTitle("Off The Cloud"))

			return "", false
		}
		// What each row opens: a folder's path, parentUp for "‹ Up", ""
		// for the line saying there is nothing.
		const parentUp = "\x00up"
		var items, targets []string
		if current != "/" {
			items = append(items, i18n.DeskFoldersPickerUp().Render(l))
			targets = append(targets, parentUp)
		}
		for _, e := range entries {
			if e.IsDir {
				items = append(items, "📁 "+e.Name)
				targets = append(targets, e.Path)
			}
		}
		if len(items) == 0 {
			items = []string{i18n.DeskFoldersPickerEmpty().Render(l)}
			targets = []string{""}
		}
		label := baseName(current)
		if label == "" {
			label = "/"
		}
		pick, err := dlgList(
			listText(i18n.DeskFoldersPickerPrompt(current, label).Render(l)),
			items, dlgTitle(i18n.DeskFoldersRemoteTitle().Render(l)), zenity.Width(460), zenity.Height(460),
			dlgOK(i18n.CommonOpen().Render(l)), dlgExtra(i18n.DeskFoldersChooseNamed(label).Render(l)),
		)
		if errors.Is(err, zenity.ErrExtraButton) {
			return current, true
		}
		if err != nil {
			return "", false
		}
		switch i := slices.Index(items, pick); {
		case i < 0 || targets[i] == "":
			continue
		case targets[i] == parentUp:
			current = parentPath(current)
		default:
			current = targets[i]
		}
	}
}
