// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"fyne.io/systray"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// Issue #132: a folder's request to be made upload only, chosen when it
// was added (addfolder.go), shows in its title until the device has it;
// the device's refusal of it on a line of its submenu. The Mac's lock on
// the folder's row (UploadOnlyBadge in PopoverView.swift).

// uploadOnlySuffix ends a folder's title with its upload-only request on
// its way, or a device that can't take it; "" once the device has it.
func uploadOnlySuffix(f config.FolderStatus) string {
	switch engine.UploadOnlyState(f.UploadOnly) {
	case engine.UploadOnlyMaking:
		return " · " + uploadOnlyMaking
	case engine.UploadOnlyLifting:
		return " · " + uploadOnlyLifting
	case engine.UploadOnlyUnsupported:
		return " · " + engine.UploadOnlyNeedsUpdate
	}

	return ""
}

const (
	uploadOnlyMaking  = "Making it upload only…"
	uploadOnlyLifting = "Turning upload only off…"
)

// addUploadOnlyInfo adds a folder's line for the device's refusal to its
// submenu, hidden until there is one.
func addUploadOnlyInfo(mi *systray.MenuItem) *systray.MenuItem {
	info := mi.AddSubMenuItem("", "")
	info.Disable()
	info.Hide()

	return info
}

// applyUploadOnly shows the device's refusal, if any (u.mu held).
func (u *ui) applyUploadOnly(fi *folderItem, f config.FolderStatus) {
	if f.UploadOnlyNote == "" {
		u.setShown(fi.uploadInfo, false)
		return
	}
	u.setTitle(fi.uploadInfo, f.UploadOnlyNote)
	u.setShown(fi.uploadInfo, true)
}
