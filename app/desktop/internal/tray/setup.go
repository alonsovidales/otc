// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"sync/atomic"

	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/flasher"
	"github.com/alonsovidales/otc/i18n"
)

// Issue #184: "Set Up a New Device…" - download the device image, write it
// to an SD card, delete the download and say how to continue. The Mac
// app's SetupWizardView is the same wizard (it hands the writing to
// Raspberry Pi Imager, being sandboxed). Its dialogs speak the menu's
// language as the wizard starts.

var setupRunning atomic.Bool

func (u *ui) setupDevice() {
	if !setupRunning.CompareAndSwap(false, true) {
		return
	}
	defer setupRunning.Store(false)
	l := lang()
	opts := []zenity.Option{zenity.Title(i18n.DeskFoldersSetupTitle().Render(l))}

	if err := zenity.Question(i18n.DeskFoldersSetupIntro().Render(l),
		append(opts, zenity.OKLabel(i18n.DeskFoldersSetupStart().Render(l)), zenity.NoIcon)...); err != nil {
		return
	}

	// 1. Download.
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
			_ = dlg.Text(i18n.DeskFoldersSetupDownloading(int(done>>20), int(total>>20)).Render(l))
		}
	})
	cancelled := ctx.Err() != nil
	cancel()
	_ = dlg.Close()
	if cancelled {
		return
	}
	if err != nil {
		u.setupError(l, err)
		return
	}

	// 2. Pick the card.
	var disk flasher.Disk
	for {
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
			labels[i] = d.LabelIn(l)
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
	if zenity.Question(i18n.DeskFoldersSetupEraseQuestion(disk.LabelIn(l)).Render(l),
		append(opts, zenity.OKLabel(i18n.DeskFoldersSetupErase().Render(l)), zenity.CancelLabel(i18n.CommonCancel().Render(l)), zenity.WarningIcon)...) != nil {
		return
	}

	// 3. Write, as administrator.
	wdlg, err := zenity.Progress(append(append(opts, buttons...), zenity.MaxValue(100), zenity.NoCancel())...)
	if err != nil {
		u.setupError(l, err)
		return
	}
	_ = wdlg.Text(i18n.DeskFoldersSetupWaitingPermission().Render(l))
	err = flasher.WriteElevatedIn(l, image, disk, func(p flasher.Progress) {
		_ = wdlg.Value(p.Percent())
		_ = wdlg.Text(p.StepIn(l))
	})
	_ = wdlg.Close()
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

func (u *ui) setupError(l string, err error) {
	_ = zenity.Error(i18n.DeskFoldersSetupStopped(errorText(l, err)).Render(l), zenity.Title(i18n.DeskFoldersSetupTitle().Render(l)))
}
