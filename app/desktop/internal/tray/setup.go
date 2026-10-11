// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/flasher"
	"github.com/alonsovidales/otc/i18n"
)

// Issue #184: "Set Up a New Device…" - download the device image, write it
// to an SD card, delete the download and say how to continue. The Mac
// app's SetupWizardView is the same wizard (it hands the writing to
// Raspberry Pi Imager, being sandboxed). Each dialog speaks the menu's
// language as it opens, and the progress texts as they change: a language
// picked in the tray's Language menu meanwhile shows from the next one on
// (one already open keeps its words).

var setupRunning atomic.Bool

func (u *ui) setupDevice() {
	if !setupRunning.CompareAndSwap(false, true) {
		return
	}
	defer setupRunning.Store(false)
	// l is the menu's language when the dialog at hand opened; read again
	// before each one (setupOpts).
	l, opts := setupOpts()

	if err := zenity.Question(i18n.DeskFoldersSetupIntro().Render(l),
		append(opts, zenity.OKLabel(i18n.DeskFoldersSetupStart().Render(l)), zenity.NoIcon)...); err != nil {
		return
	}

	// 1. Download.
	l, opts = setupOpts()
	// The buttons' words too: Windows' own dialogs say OK and Cancel in
	// English otherwise.
	buttons := []zenity.Option{zenity.OKLabel(i18n.CommonOk().Render(l)), zenity.CancelLabel(i18n.CommonCancel().Render(l))}
	dlg, err := zenity.Progress(append(append(opts, buttons...), zenity.MaxValue(100))...)
	if err != nil {
		u.setupError(l, err)
		return
	}
	_ = dlg.Text(i18n.DeskFoldersSetupPreparingDownload().Render(l))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-dlg.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	image, err := flasher.Download(ctx, func(done, total int64) {
		if total > 0 {
			_ = dlg.Value(int(done * 100 / total))
			_ = dlg.Text(i18n.DeskFoldersSetupDownloading(int(done>>20), int(total>>20)).Render(lang()))
		}
	})
	cancelled := ctx.Err() != nil
	cancel()
	_ = dlg.Close()
	if cancelled {
		return
	}
	if err != nil {
		u.setupError(lang(), err)
		return
	}

	// 2. Pick the card.
	var disk flasher.Disk
	for {
		l, opts = setupOpts()
		disks, err := flasher.ListDisks()
		if err != nil {
			u.setupError(l, err)
			return
		}
		if len(disks) == 0 {
			if zenity.Question(i18n.DeskFoldersSetupInsertCard().Render(l),
				append(opts, zenity.OKLabel(i18n.DeskFoldersTryAgain().Render(l)), zenity.InfoIcon)...) != nil {
				return
			}
			continue
		}
		labels := make([]string, len(disks))
		for i, d := range disks {
			labels[i] = cardRow(l, d)
		}
		choice, err := zenity.List(listText(i18n.DeskFoldersSetupChooseCard().Render(l)), labels,
			append(opts, zenity.OKLabel(i18n.CommonNext().Render(l)), zenity.ExtraButton(i18n.CommonRefresh().Render(l)),
				zenity.CancelLabel(i18n.CommonCancel().Render(l)), zenity.DisallowEmpty())...)
		if errors.Is(err, zenity.ErrExtraButton) {
			continue
		}
		if err != nil {
			return
		}
		// The row picked, by its index.
		i := slices.Index(labels, choice)
		if i < 0 {
			continue
		}
		disk = disks[i]
		if disk.Size < flasher.MinCardSize {
			_ = zenity.Warning(i18n.DeskFoldersSetupCardTooSmall(disk.LabelIn(l)).Render(l), opts...)
			disk = flasher.Disk{}
			continue
		}
		break
	}
	l, opts = setupOpts()
	if zenity.Question(i18n.DeskFoldersSetupEraseQuestion(disk.LabelIn(l)).Render(l),
		append(opts, zenity.OKLabel(i18n.DeskFoldersSetupErase().Render(l)), zenity.CancelLabel(i18n.CommonCancel().Render(l)), zenity.WarningIcon)...) != nil {
		return
	}

	// 3. Write, as administrator.
	l, opts = setupOpts()
	buttons = []zenity.Option{zenity.OKLabel(i18n.CommonOk().Render(l)), zenity.CancelLabel(i18n.CommonCancel().Render(l))}
	wdlg, err := zenity.Progress(append(append(opts, buttons...), zenity.MaxValue(100), zenity.NoCancel())...)
	if err != nil {
		u.setupError(l, err)
		return
	}
	_ = wdlg.Text(i18n.DeskFoldersSetupWaitingPermission().Render(l))
	err = flasher.WriteElevatedIn(l, image, disk, func(p flasher.Progress) {
		_ = wdlg.Value(p.Percent())
		_ = wdlg.Text(p.StepIn(lang()))
	})
	_ = wdlg.Close()
	l, opts = setupOpts()
	if errors.Is(err, flasher.ErrCancelled) {
		_ = zenity.Info(i18n.DeskFoldersSetupNotWritten(i18n.DeskMainSetUpDevice().Render(l)).Render(l), opts...)
		return
	}
	if err != nil {
		u.setupError(l, err)
		return
	}

	// 4. The download is no longer needed.
	_ = os.Remove(image)

	// 5. How to continue.
	steps := flasher.NextStepsIn(l)
	if runtime.GOOS == "windows" {
		steps += "\n\n" + i18n.DeskFoldersSetupWindowsFormat().Render(l)
	}
	_ = zenity.Info(steps, append(opts, zenity.Title(i18n.DeskFoldersSetupCardReadyTitle().Render(l)))...)
}

// setupOpts is the menu's language now and the options every setup dialog
// opens with (its title) in it.
func setupOpts() (string, []zenity.Option) {
	l := lang()

	return l, []zenity.Option{zenity.Title(i18n.DeskFoldersSetupTitle().Render(l))}
}

// cardRowRunes is the most characters a card's row has on Windows, whose
// list (zenity's) is 241 pixels wide and cuts what doesn't fit: about 36
// characters of the dialogs' font (Segoe UI 9 pt) in capitals, kept a
// little below for a wider font.
const cardRowRunes = 34

// cardRow is a card's row in the list of cards, in lang: its label on
// Linux, whose list grows to fit it, and windowsCardRow on Windows.
func cardRow(lang string, d flasher.Disk) string {
	if runtime.GOOS != "windows" {
		return d.LabelIn(lang)
	}

	return windowsCardRow(lang, d)
}

// windowsCardRow is a card's row in Windows' narrow list: the size and the
// disk's number first, where the list never cuts them, then the name,
// shortened when the row would be too wide ("31.9 GB · disk 2 · Generic
// STORAGE…"). Rows stay distinct, each disk having its own number.
func windowsCardRow(lang string, d flasher.Disk) string {
	name := d.Name
	if name == "" {
		name = i18n.DeskFoldersFlashUnnamedDisk().Render(lang)
	}
	head := flasher.HumanSizeIn(lang, d.Size) + " · " + d.ShortIDIn(lang) + " · "

	return head + shorten(name, cardRowRunes-utf8.RuneCountInString(head))
}

// shorten is s in at most n characters (never fewer than two), ending in
// "…" when it was cut.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= max(n, 2) {
		return s
	}

	return strings.TrimRight(string(r[:max(n-1, 1)]), " ") + "…"
}

func (u *ui) setupError(l string, err error) {
	_ = zenity.Error(i18n.DeskFoldersSetupStopped(errorText(l, err)).Render(l), zenity.Title(i18n.DeskFoldersSetupTitle().Render(l)))
}
