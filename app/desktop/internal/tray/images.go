// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"path/filepath"

	"fyne.io/systray"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
	"github.com/alonsovidales/otc/i18n"
)

// Issue #192: keeping a folder out of Images - an option when a folder
// from this computer is added (addfolder.go), and a checkbox in each
// folder's submenu. The menu only records the request in config.json; the
// engine sends it to the device (engine/out_of_images.go). The Mac's
// AddFolderChooser toggle and the eye button on each folder's row
// (PopoverView.swift).

// imagesExplainKinds closes "What Do These Do?" (explainText's last
// paragraph, desk.main.explain_images): the one explanation, with the
// folder it is about named - "here" points at nothing there. Its English,
// which the tests read.
var imagesExplainKinds = i18n.DeskMainExplainImages().Render("")

// imagesSuffix ends a folder's title with how it stands with Images, in the
// menu's language.
func imagesSuffix(f config.FolderStatus) string {
	l := lang()
	switch engine.ImagesState(f.OutOfImages) {
	case engine.ImagesKeptOut, engine.ImagesKeptOutByParent:
		return " · " + i18n.DeskFoldersImagesKeptOut().Render(l)
	case engine.ImagesKeeping:
		return " · " + i18n.DeskFoldersImagesKeeping().Render(l)
	case engine.ImagesShowing:
		return " · " + i18n.DeskFoldersImagesShowing().Render(l)
	case engine.ImagesUnsupported:
		return " · " + i18n.DeskMainImagesNeedsUpdate().Render(l)
	}

	return ""
}

// applyImages sets a folder's "Kept Out of Images" checkbox and the line
// under it from its status (u.mu held). Hidden while unknown - and on a
// device that can't, unless a request is waiting for its update.
func (u *ui) applyImages(fi *folderItem, f config.FolderStatus) {
	l := lang()
	// The checkbox's own words too: a language picked meanwhile shows at
	// this apply (only a change is written).
	u.setTitle(fi.images, i18n.DeskFoldersImagesCheckbox().Render(l))
	info := ""
	switch state := engine.ImagesState(f.OutOfImages); state {
	case engine.ImagesUnknown:
		u.setShown(fi.images, false)
	case engine.ImagesUnsupported:
		needs := i18n.DeskMainImagesNeedsUpdate().Render(l)
		u.setChecked(fi.images, false)
		u.setEnabled(fi.images, false)
		u.setTip(fi.images, needs)
		u.setShown(fi.images, true)
		info = needs
	case engine.ImagesKeptOutByParent:
		// Showing it is refused until the folder above is shown.
		by := i18n.DeskMainImagesByParent(f.OutOfImagesBy).Render(l)
		u.setChecked(fi.images, true)
		u.setEnabled(fi.images, false)
		u.setTip(fi.images, by)
		u.setShown(fi.images, true)
		info = by
	default:
		u.setChecked(fi.images, state.Kept())
		u.setEnabled(fi.images, true)
		u.setTip(fi.images, i18n.DeskFoldersImagesExplain().Render(l))
		u.setShown(fi.images, true)
	}
	if f.OutOfImagesNote != "" {
		info = f.OutOfImagesNote // the device's refusal of the last request
	}
	if info == "" {
		u.setShown(fi.imagesInfo, false)
		return
	}
	u.setTitle(fi.imagesInfo, info)
	u.setShown(fi.imagesInfo, true)
}

// toggleImages is a click on a folder's checkbox: asked first - keeping a
// folder out deletes the tags and faces found in it, showing it again may
// search it for faces - then recorded for the engine to send.
func (u *ui) toggleImages(fi *folderItem) {
	var f *config.FolderStatus
	st := u.c.Snapshot()
	for _, s := range append(st.Folders, st.RemoteFolders...) {
		if s.ID == fi.id {
			s := s
			f = &s
		}
	}
	if f == nil {
		return
	}
	state := engine.ImagesState(f.OutOfImages)
	if state == engine.ImagesUnknown || state == engine.ImagesUnsupported || state == engine.ImagesKeptOutByParent {
		return
	}
	name := filepath.Base(f.Path)
	keepOut := !state.Kept()
	l := lang()
	title, message, ok := i18n.DeskMainImagesShowTitle(name), i18n.DeskMainImagesShowMessage(), i18n.DeskMainImagesShowButton()
	if keepOut {
		title, message, ok = i18n.DeskMainImagesKeepTitle(name), i18n.DeskMainImagesKeepMessage(), i18n.DeskMainImagesKeepButton()
	}
	if dlgQuestion(message.Render(l), dlgTitle(title.Render(l)), dlgOK(ok.Render(l)),
		dlgCancel(i18n.CommonCancel().Render(l))) != nil {
		return
	}
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	if !cfg.SetOutOfImagesRequest(fi.id, keepOut) {
		return // removed meanwhile
	}
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = dlgError(errorText(lang(), err), dlgTitle("Off The Cloud"))
	}
	Refresh()
}

// addImagesItems adds a folder's checkbox and the line under it to its
// submenu, both hidden until apply knows its state (and writes their words
// in the language then in effect).
func addImagesItems(mi *systray.MenuItem) (images, info *systray.MenuItem) {
	l := lang()
	images = addSubMenuItemCheckbox(mi, i18n.DeskFoldersImagesCheckbox().Render(l), i18n.DeskFoldersImagesExplain().Render(l), false)
	images.Hide()
	info = addSubMenuItem(mi, "", "")
	info.Disable()
	info.Hide()

	return images, info
}
