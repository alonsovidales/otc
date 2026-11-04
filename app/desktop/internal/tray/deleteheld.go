// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/i18n"
)

// "Delete Them on the Device Too…": while the device-side mass-deletion
// guard holds a two-way folder's deletes (its title says "25 files deleted
// here at once are kept on the device - …"), its submenu offers to delete
// them there too. A click asks first, with the count state.json has, and a
// yes records the key of that very set in config.json (the folder's
// DeleteHeld) for the engine's next pass, which runs at once
// (engine/deleteheld.go): files gone here since are asked about again, not
// deleted with them. The Mac's button under the folder's row
// (DeleteHeldButton in PopoverView.swift); `otc-sync delete-held`.

// addDeleteHeldItem adds a folder's "Delete Them on the Device Too…" to its
// submenu, hidden until the guard holds something there.
func addDeleteHeldItem(mi *systray.MenuItem) *systray.MenuItem {
	l := lang()
	item := addSubMenuItem(mi, i18n.DeskMainDeleteHeld().Render(l), i18n.DeskMainDeleteHeldTip().Render(l))
	item.Hide()

	return item
}

// deleteHeldShown: the guard holds deletes of this two-way folder, and
// the engine said which (a key to approve).
func deleteHeldShown(f config.FolderStatus) bool {
	return f.RemotePath != "" && f.DeletesHeld > 0 && f.DeletesHeldKey != ""
}

// applyDeleteHeld shows or hides the item, in the menu's language (u.mu
// held; only a change is written).
func (u *ui) applyDeleteHeld(fi *folderItem, f config.FolderStatus) {
	l := lang()
	u.setTitle(fi.deleteHeld, i18n.DeskMainDeleteHeld().Render(l))
	u.setTip(fi.deleteHeld, i18n.DeskMainDeleteHeldTip().Render(l))
	u.setShown(fi.deleteHeld, deleteHeldShown(f))
}

// deleteHeldOptions are the question's dialog options for n files: its
// title and buttons in the menu's language, a warning, and Cancel the
// default (a test holds it there) - a stray Enter never deletes.
func deleteHeldOptions(n int) []zenity.Option {
	l := lang()

	return []zenity.Option{
		dlgTitle(i18n.DeskMainDeleteHeldTitle(n).Render(l)),
		dlgOK(i18n.DeskMainDeleteHeldButton().Render(l)),
		dlgCancel(i18n.CommonCancel().Render(l)),
		zenity.WarningIcon,
		zenity.DefaultCancel(),
	}
}

// deleteHeld is a click on the item: the set the snapshot shows now is the
// one asked about, and the one a yes approves.
func (u *ui) deleteHeld(fi *folderItem) {
	f, ok := findFolder(u.c.Snapshot(), fi.id)
	if !ok || !deleteHeldShown(f) {
		return // nothing held any more, or the folder removed meanwhile
	}
	n, key := f.DeletesHeld, f.DeletesHeldKey
	if dlgQuestion(i18n.DeskMainDeleteHeldMessage().Render(lang()), deleteHeldOptions(n)...) != nil {
		return
	}
	// false: removed meanwhile.
	u.edit(func(cfg *config.Config) (bool, error) { return cfg.SetDeleteHeldRequest(fi.id, key), nil })
}
