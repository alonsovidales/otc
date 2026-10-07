// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"path/filepath"

	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// Issue #192: keeping a folder out of Images - asked when a folder is
// added, and a checkbox in each folder's submenu. The menu only records
// the request in config.json; the engine sends it to the device
// (engine/out_of_images.go). The Mac's AddFolderChooser toggle and the eye
// button on each folder's row (PopoverView.swift).

const (
	imagesKeepMessage = "Its photos and videos won't be tagged, searched for faces or shown in Images, and the tags and faces already found in them are deleted. Files still shows them."
	imagesShowMessage = "Its photos and videos go back to Images, and are tagged - and searched for faces, if face recognition is on - in the background."
	// imagesExplainKinds closes "What Do These Do?": the one explanation,
	// with the folder it is about named - "here" points at nothing there.
	imagesExplainKinds = "Keep out of Images (asked when you add a folder)\n" +
		"Photos and videos in a folder kept out of Images aren't tagged, searched for faces or shown in Images. Files still shows them. " +
		"Any tags and faces already found in them are deleted. You can change this later from the folder's menu. " +
		"A folder renamed later is a new folder on the device: keep it out of Images again."
	// imagesAddLater answers the add question's "no": nothing is sent, so
	// a folder the device already keeps out stays so - "Show in Images"
	// would promise what this button doesn't do.
	imagesAddLater = "Not Now"
)

func imagesKeepTitle(name string) string { return "Keep “" + name + "” out of Images?" }

func imagesShowTitle(name string) string { return "Show “" + name + "” in Images?" }

// imagesAddQuestion is the add flow's question for the folder called name.
func imagesAddQuestion(name string) string {
	return "Keep the photos and videos in “" + name + "” out of Images?\n\n" + engine.OutOfImagesAddCaption +
		" You can change this later from the folder's menu."
}

func imagesByParentTip(parent string) string {
	return "Inside " + parent + ", which is kept out of Images"
}

// askKeepOutOfImages is the add flow's question for the folder called
// name: a request to keep it out, or nil to add it as usual - nothing sent
// (the other button, or the dialog closed). Not asked of a device known to
// be unable.
func (u *ui) askKeepOutOfImages(name string) *bool {
	if u.c.Snapshot().OutOfImagesUnsupported {
		return nil
	}
	err := zenity.Question(imagesAddQuestion(name),
		zenity.Title("Off The Cloud"), zenity.OKLabel("Keep Out of Images"), zenity.CancelLabel(imagesAddLater))
	if err != nil {
		return nil
	}
	keep := true

	return &keep
}

// imagesSuffix ends a folder's title with how it stands with Images.
func imagesSuffix(f config.FolderStatus) string {
	switch engine.ImagesState(f.OutOfImages) {
	case engine.ImagesKeptOut, engine.ImagesKeptOutByParent:
		return " · Kept out of Images"
	case engine.ImagesKeeping:
		return " · Keeping out of Images…"
	case engine.ImagesShowing:
		return " · Showing in Images…"
	case engine.ImagesUnsupported:
		return " · " + engine.OutOfImagesNeedsUpdate
	}

	return ""
}

// applyImages sets a folder's "Kept Out of Images" checkbox and the line
// under it from its status (u.mu held). Hidden while unknown - and on a
// device that can't, unless a request is waiting for its update.
func (u *ui) applyImages(fi *folderItem, f config.FolderStatus) {
	info := ""
	switch state := engine.ImagesState(f.OutOfImages); state {
	case engine.ImagesUnknown:
		u.setShown(fi.images, false)
	case engine.ImagesUnsupported:
		u.setChecked(fi.images, false)
		u.setEnabled(fi.images, false)
		u.setTip(fi.images, engine.OutOfImagesNeedsUpdate)
		u.setShown(fi.images, true)
		info = engine.OutOfImagesNeedsUpdate
	case engine.ImagesKeptOutByParent:
		// Showing it is refused until the folder above is shown.
		u.setChecked(fi.images, true)
		u.setEnabled(fi.images, false)
		u.setTip(fi.images, imagesByParentTip(f.OutOfImagesBy))
		u.setShown(fi.images, true)
		info = imagesByParentTip(f.OutOfImagesBy)
	default:
		u.setChecked(fi.images, state.Kept())
		u.setEnabled(fi.images, true)
		u.setTip(fi.images, engine.OutOfImagesExplain)
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
	title, message, ok := imagesShowTitle(name), imagesShowMessage, "Show in Images"
	if keepOut {
		title, message, ok = imagesKeepTitle(name), imagesKeepMessage, "Keep Out of Images"
	}
	if zenity.Question(message, zenity.Title(title), zenity.OKLabel(ok), zenity.CancelLabel("Cancel")) != nil {
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
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}

// addImagesItems adds a folder's checkbox and the line under it to its
// submenu, both hidden until apply knows its state.
func addImagesItems(mi *systray.MenuItem) (images, info *systray.MenuItem) {
	images = mi.AddSubMenuItemCheckbox("Kept Out of Images", engine.OutOfImagesExplain, false)
	images.Hide()
	info = mi.AddSubMenuItem("", "")
	info.Disable()
	info.Hide()

	return images, info
}
