// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"errors"
	"runtime"
	"slices"
	"strings"

	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// "Add Folder…" in two steps, as the Mac's AddFolderChooser: first the
// kind of folder, then the options that kind has - a backup: Keep out of
// Images (upload only it always is); two-way from this computer: Upload
// only and Keep out of Images; from the device: none, it keeps what it has
// there - and only then the folder itself. The options are recorded with
// the folder as requests the engine sends to the device (requests.go).

// folderKind is what step 1 picks.
type folderKind int

const (
	kindBackup folderKind = iota
	kindLocal
	kindDevice
)

// The kinds' words, the Mac's with "this computer" for "this Mac".
var kindTitles = map[folderKind]string{
	kindBackup: "Back up a folder from this computer",
	kindLocal:  "Sync a folder from this computer",
	kindDevice: "Sync a folder from the device",
}

var kindSubtitles = map[folderKind]string{
	kindBackup: "One way: this computer → device (upload only, no deletes)",
	kindLocal:  "Two ways: starts from this computer",
	kindDevice: "Two ways: starts from the device",
}

// addOption is one of step 2's checkboxes.
type addOption int

const (
	optUploadOnly addOption = iota
	optKeepOut
)

var optionLabels = map[addOption]string{
	optUploadOnly: engine.UploadOnlyLabel,
	optKeepOut:    "Keep out of Images",
}

// optionExplain is what an option does, as the Mac's caption and (i)
// together.
func optionExplain(o addOption) string {
	if o == optUploadOnly {
		return engine.UploadOnlyCaption + " " + engine.UploadOnlyTwoWay + " " + engine.UploadOnlyLater
	}

	return engine.OutOfImagesAddCaption + " " + imagesAddLater
}

// imagesAddLater is Keep out of Images' (i) in the add flow.
const imagesAddLater = "You can change it later from the folder's menu. A folder renamed later is a new folder on the device: keep it out of Images again."

// kindOptions is what step 2 offers for kind k: nothing a device known to
// be unable can take, and nothing for a folder from the device.
func kindOptions(k folderKind, st config.State) []addOption {
	var opts []addOption
	if k == kindLocal && !st.UploadOnlyUnsupported {
		opts = append(opts, optUploadOnly)
	}
	if k != kindDevice && !st.OutOfImagesUnsupported {
		opts = append(opts, optKeepOut)
	}

	return opts
}

// optionsText is the checklist's text: the kind, then each option said in
// full - and for a backup, that it is always upload only.
func optionsText(k folderKind, opts []addOption) string {
	var b strings.Builder
	b.WriteString(kindTitles[k] + ". " + kindSubtitles[k] + ".\n\nTick what you want, then choose the folder.")
	if k == kindBackup {
		b.WriteString("\n\n" + engine.UploadOnlyLabel + ": " + strings.ToLower(engine.UploadOnlyBackup[:1]) + engine.UploadOnlyBackup[1:] + " " + engine.UploadOnlyCaption)
	}
	for _, o := range opts {
		b.WriteString("\n\n" + optionLabels[o] + ": " + optionExplain(o))
	}

	return b.String()
}

// optionQuestion is an option asked on its own, where the desktop's list
// has no checkboxes (Windows).
func optionQuestion(k folderKind, o addOption) string {
	q := "Keep the folder out of Images?"
	if o == optUploadOnly {
		q = "Make the folder upload only?"
	}
	q += "\n\n" + optionExplain(o)
	if k == kindBackup {
		q += "\n\n(" + engine.UploadOnlyLabel + " is always on for a backup.)"
	}

	return q
}

// questionAnswer is an option's Yes/No/Cancel question answered: Yes,
// No (the extra button), or an error to stop the whole flow - Cancel, the
// dialog closed, or no dialog at all.
func questionAnswer(err error) (yes bool, stop error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, zenity.ErrExtraButton):
		return false, nil
	default:
		return false, err
	}
}

// errBack: step 2's Back.
var errBack = errors.New("back")

// checklists: the desktop's list dialog has checkboxes (zenity on Linux);
// Windows' has none, so each option is a question of its own there.
var checklists = runtime.GOOS != "windows" && runtime.GOOS != "darwin"

// addFolder is "Add Folder…": the kind, its options, then the folder.
func (u *ui) addFolder() {
	for {
		k, ok := chooseKind()
		if !ok {
			return
		}
		if k == kindDevice {
			u.addRemote()

			return
		}
		chosen, err := chooseOptions(k, kindOptions(k, u.c.Snapshot()))
		if errors.Is(err, errBack) {
			continue
		}
		if err != nil {
			return
		}
		u.addLocalFolder(k, chosen)

		return
	}
}

// chooseKind is step 1.
func chooseKind() (folderKind, bool) {
	kinds := []folderKind{kindBackup, kindLocal, kindDevice}
	items := make([]string, len(kinds))
	for i, k := range kinds {
		items[i] = kindTitles[k]
		if checklists {
			// Room for the subtitle; Windows' list is too narrow for it.
			items[i] += " — " + kindSubtitles[k]
		}
	}
	for {
		pick, err := zenity.List("Choose what kind of folder to add:", items,
			zenity.Title("Add a Folder"), zenity.OKLabel("Next"), zenity.CancelLabel("Cancel"),
			zenity.DisallowEmpty(), zenity.Width(620), zenity.Height(260))
		if err != nil {
			return 0, false
		}
		if i := slices.Index(items, pick); i >= 0 {
			return kinds[i], true
		}
		// Next with nothing picked: asked again.
	}
}

// chooseOptions is step 2: the options ticked, errBack for Back, or an
// error (Cancel) to stop. With no option to offer it asks nothing.
func chooseOptions(k folderKind, opts []addOption) (map[addOption]bool, error) {
	chosen := map[addOption]bool{}
	if len(opts) == 0 {
		return chosen, nil
	}
	if !checklists {
		for _, o := range opts {
			// Yes, No and Cancel (MB_YESNOCANCEL): Cancel is the dialog's
			// own cancel button, so closing it or Esc stops too, rather
			// than meaning No.
			yes, err := questionAnswer(zenity.Question(optionQuestion(k, o), zenity.Title(kindTitles[k]),
				zenity.OKLabel("Yes"), zenity.ExtraButton("No"), zenity.CancelLabel("Cancel")))
			if err != nil {
				return nil, err
			}
			chosen[o] = yes
		}

		return chosen, nil
	}
	items := make([]string, len(opts))
	for i, o := range opts {
		items[i] = optionLabels[o]
	}
	picked, err := zenity.ListMultiple(optionsText(k, opts), items, zenity.CheckList(),
		zenity.Title(kindTitles[k]), zenity.OKLabel("Choose Folder…"), zenity.CancelLabel("Cancel"),
		zenity.ExtraButton("Back"), zenity.Width(620), zenity.Height(480))
	if errors.Is(err, zenity.ErrExtraButton) {
		return nil, errBack
	}
	if err != nil {
		return nil, err
	}
	for i, o := range opts {
		chosen[o] = slices.Contains(picked, items[i])
	}

	return chosen, nil
}

// addLocalFolder is the end of the flow for a folder from this computer:
// the folder chooser, then the folder and its requests in config.json.
func (u *ui) addLocalFolder(k folderKind, chosen map[addOption]bool) {
	title := "Choose a folder to keep in sync"
	if k == kindBackup {
		title = "Choose a folder to back up to the device"
	}
	dir, err := zenity.SelectFile(zenity.Directory(), zenity.Title(title))
	if err != nil || dir == "" {
		return
	}
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	// A two-way folder from here is made two-way by the engine
	// (migrateFolders), its requests with it.
	cfg.Folders = append(cfg.Folders, newLocalFolder(dir, k, chosen))
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}

// newLocalFolder is the config entry for dir: a backup is one way (and
// upload only at every start, so never asked for it); an option left
// unticked sends nothing.
func newLocalFolder(dir string, k folderKind, chosen map[addOption]bool) config.Folder {
	f := config.Folder{ID: config.NewID(), Path: dir, OneWay: k == kindBackup}
	if chosen[optKeepOut] {
		keep := true
		f.OutOfImages = &keep
	}
	if chosen[optUploadOnly] && k == kindLocal {
		on := true
		f.UploadOnly = &on
	}

	return f
}
