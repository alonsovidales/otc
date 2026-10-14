// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"errors"
	"io/fs"
	"path/filepath"

	"fyne.io/systray"

	"github.com/alonsovidales/otc/app/desktop/internal/browser"
	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/i18n"
)

// Issue #197: "Open", the first item of each folder's submenu, shows the
// folder on this computer in the system's file manager - a backup's own
// folder, a two-way folder's local one, from this computer or from the
// device alike. The Mac's click on a folder's icon or name in its panel
// (FolderOpenArea in PopoverView.swift): a tray item that has a submenu
// takes no click of its own on Windows or Linux, hence the item. A folder
// that is gone is only reported, with where it was expected; nothing is
// removed or changed here (the next pass already takes a two-way folder
// whose folder is gone off the list, leaving the device alone).

// addOpenItem adds a folder's Open item, first in its submenu.
func addOpenItem(mi *systray.MenuItem, f config.FolderStatus) *systray.MenuItem {
	l := lang()

	return addSubMenuItem(mi, i18n.CommonOpen().Render(l), i18n.DeskMainOpenFolderTip(f.Path).Render(l))
}

// applyOpen writes the Open item's words in the menu's language (u.mu
// held; only a change is written).
func (u *ui) applyOpen(fi *folderItem, f config.FolderStatus) {
	l := lang()
	u.setTitle(fi.open, i18n.CommonOpen().Render(l))
	u.setTip(fi.open, i18n.DeskMainOpenFolderTip(f.Path).Render(l))
}

// openFolderAt is browser.OpenFolder; the tests replace it.
var openFolderAt = browser.OpenFolder

// openFolder is a click on a folder's Open: its path as the snapshot has
// it now, the same in a viewer tray next to the service.
func (u *ui) openFolder(fi *folderItem) {
	f, ok := findFolder(u.c.Snapshot(), fi.id)
	if !ok {
		return // removed meanwhile
	}
	if err := openFolderAt(f.Path); err != nil {
		_ = dlgError(openFolderError(lang(), f, err), dlgTitle("Off The Cloud"))
	}
}

// findFolder is the folder with this id, of either kind.
func findFolder(st config.State, id string) (config.FolderStatus, bool) {
	for _, list := range [][]config.FolderStatus{st.Folders, st.RemoteFolders} {
		for _, f := range list {
			if f.ID == id {
				return f, true
			}
		}
	}

	return config.FolderStatus{}, false
}

// openFolderError is what the dialog says, in lang: the folder isn't where
// it was (gone, or a file in its place), or the system's reason the file
// manager didn't start - with the path, to open it by hand.
func openFolderError(lang string, f config.FolderStatus, err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return i18n.DeskMainFolderMissing(filepath.Base(f.Path), f.Path).Render(lang)
	}

	return i18n.DeskMainOpenFolderFailed(err.Error(), f.Path).Render(lang)
}
