// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"encoding/xml"
	"errors"
	"runtime"
	"slices"
	"strings"

	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/i18n"
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

// The kinds' words, the Mac's with "this computer" for "this Mac": the
// catalog's messages, rendered in the menu's language as a dialog opens.
var kindTitles = map[folderKind]i18n.Msg{
	kindBackup: i18n.DeskFoldersKindBackup(),
	kindLocal:  i18n.DeskFoldersKindLocal(),
	kindDevice: i18n.DeskMainKindDevice(),
}

var kindSubtitles = map[folderKind]i18n.Msg{
	kindBackup: i18n.DeskFoldersKindBackupSubtitle(),
	kindLocal:  i18n.DeskFoldersKindLocalSubtitle(),
	kindDevice: i18n.DeskMainKindDeviceSubtitle(),
}

// addOption is one of step 2's checkboxes.
type addOption int

const (
	optUploadOnly addOption = iota
	optKeepOut
)

var optionLabels = map[addOption]i18n.Msg{
	optUploadOnly: i18n.DeskMainUploadOnly(),
	optKeepOut:    i18n.DeskMainImagesKeepOut(),
}

// optionExplain is what an option does in lang, as the Mac's caption and
// (i) together: whole sentences, each a key of its own (the caption is
// the Mac's too).
func optionExplain(lang string, o addOption) string {
	if o == optUploadOnly {
		return i18n.DeskMainUploadOnlyCaption().Render(lang) + " " +
			i18n.DeskFoldersUploadOnlyTwoWay().Render(lang) + " " +
			i18n.DeskFoldersUploadOnlyLater().Render(lang)
	}

	return i18n.DeskMainImagesAddCaption().Render(lang) + " " + i18n.DeskFoldersImagesAddLater().Render(lang)
}

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

// optionsText is the checklist's text, in the menu's language: the kind,
// then each option said in full - and for a backup, that it is always
// upload only. One paragraph each.
func optionsText(k folderKind, opts []addOption) string {
	l := lang()
	var b strings.Builder
	b.WriteString(i18n.DeskFoldersOptionsIntro(kindTitles[k].Render(l), kindSubtitles[k].Render(l)).Render(l))
	if k == kindBackup {
		b.WriteString("\n\n" + i18n.DeskFoldersOptionsBackupLine(optionLabels[optUploadOnly].Render(l), i18n.DeskMainUploadOnlyCaption().Render(l)).Render(l))
	}
	for _, o := range opts {
		b.WriteString("\n\n" + i18n.DeskFoldersOptionsLine(optionLabels[o].Render(l), optionExplain(l, o)).Render(l))
	}

	return b.String()
}

// optionQuestion is an option asked on its own, where the desktop's list
// has no checkboxes (Windows), in the menu's language.
func optionQuestion(k folderKind, o addOption) string {
	l := lang()
	q := i18n.DeskFoldersAskKeepOut().Render(l)
	if o == optUploadOnly {
		q = i18n.DeskFoldersAskUploadOnly().Render(l)
	}
	q += "\n\n" + optionExplain(l, o)
	if k == kindBackup {
		q += "\n\n" + i18n.DeskFoldersQuestionBackupNote(optionLabels[optUploadOnly].Render(l)).Render(l)
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

// listText is a list dialog's text as zenity takes it. On Linux zenity
// reads it as Pango markup - ncruces/zenity opens its message dialogs with
// --no-markup, not its lists - so a folder name or a translation holding
// & or < is escaped there (docs/i18n.md, "Rendering contract"); Windows
// shows it as it is. Rows are plain text, and a pick maps back to its row
// by index.
func listText(s string) string {
	if !checklists {
		return s
	}
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}

	return b.String()
}

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
	l := lang()
	kinds := []folderKind{kindBackup, kindLocal, kindDevice}
	items := kindItems(l, kinds, checklists)
	for {
		pick, err := dlgList(listText(i18n.DeskFoldersAddPrompt().Render(l)), items,
			dlgTitle(i18n.DeskFoldersAddTitle().Render(l)), dlgOK(i18n.CommonNext().Render(l)),
			dlgCancel(i18n.CommonCancel().Render(l)),
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

// kindItems are step 1's rows in lang: each kind's title, with its
// subtitle where the list has room for it (Windows' is too narrow).
func kindItems(lang string, kinds []folderKind, withSubtitles bool) []string {
	items := make([]string, len(kinds))
	for i, k := range kinds {
		items[i] = kindTitles[k].Render(lang)
		if withSubtitles {
			items[i] = i18n.DeskFoldersKindRow(items[i], kindSubtitles[k].Render(lang)).Render(lang)
		}
	}

	return items
}

// chooseOptions is step 2: the options ticked, errBack for Back, or an
// error (Cancel) to stop. With no option to offer it asks nothing.
func chooseOptions(k folderKind, opts []addOption) (map[addOption]bool, error) {
	chosen := map[addOption]bool{}
	if len(opts) == 0 {
		return chosen, nil
	}
	l := lang()
	if !checklists {
		for _, o := range opts {
			// Yes, No and Cancel (MB_YESNOCANCEL): Cancel is the dialog's
			// own cancel button, so closing it or Esc stops too, rather
			// than meaning No.
			yes, err := questionAnswer(dlgQuestion(optionQuestion(k, o), dlgTitle(kindTitles[k].Render(l)),
				dlgOK(i18n.DeskFoldersYes().Render(l)), dlgExtra(i18n.DeskFoldersNo().Render(l)),
				dlgCancel(i18n.CommonCancel().Render(l))))
			if err != nil {
				return nil, err
			}
			chosen[o] = yes
		}

		return chosen, nil
	}
	items := make([]string, len(opts))
	for i, o := range opts {
		items[i] = optionLabels[o].Render(l)
	}
	picked, err := dlgListMultiple(listText(optionsText(k, opts)), items, zenity.CheckList(),
		dlgTitle(kindTitles[k].Render(l)), dlgOK(i18n.DeskMainChooseFolder().Render(l)),
		dlgCancel(i18n.CommonCancel().Render(l)),
		dlgExtra(i18n.CommonBack().Render(l)), zenity.Width(620), zenity.Height(480))
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
	title := i18n.DeskFoldersChooseSyncFolder()
	if k == kindBackup {
		title = i18n.DeskFoldersChooseBackupFolder()
	}
	dir, err := zenity.SelectFile(zenity.Directory(), dlgTitle(title.Render(lang())))
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
		_ = dlgError(errorText(lang(), err), dlgTitle("Off The Cloud"))
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
