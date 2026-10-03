// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/flasher"
)

// Issue #184: "Set Up a New Device…" - download the device image, write it
// to an SD card, delete the download and say how to continue. The Mac
// app's SetupWizardView is the same wizard (it hands the writing to
// Raspberry Pi Imager, being sandboxed).

var setupRunning atomic.Bool

const setupTitle = "Set Up a New Device"

func (u *ui) setupDevice() {
	if !setupRunning.CompareAndSwap(false, true) {
		return
	}
	defer setupRunning.Store(false)
	opts := []zenity.Option{zenity.Title(setupTitle)}

	if err := zenity.Question("This prepares the SD card for a new Off The Cloud device:\n\n"+
		"1. Download the device image (about 560 MB) and check its signature.\n"+
		"2. Write it to an SD card of 8 GB or more - everything on the card is erased.\n"+
		"3. Delete the download.\n\n"+
		"You need a Raspberry Pi 5 with 8 GB of RAM, the SD card and two USB disks.",
		append(opts, zenity.OKLabel("Start"), zenity.NoIcon)...); err != nil {
		return
	}

	// 1. Download.
	dlg, err := zenity.Progress(append(opts, zenity.MaxValue(100))...)
	if err != nil {
		u.setupError(err)
		return
	}
	_ = dlg.Text("Preparing the download…")
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
			_ = dlg.Text(fmt.Sprintf("Downloading… %d of %d MB", done>>20, total>>20))
		}
	})
	cancelled := ctx.Err() != nil
	cancel()
	_ = dlg.Close()
	if cancelled {
		return
	}
	if err != nil {
		u.setupError(err)
		return
	}

	// 2. Pick the card.
	var disk flasher.Disk
	for {
		disks, err := flasher.ListDisks()
		if err != nil {
			u.setupError(err)
			return
		}
		if len(disks) == 0 {
			if zenity.Question("Insert the SD card (in the computer's card slot or a USB card reader), then try again.",
				append(opts, zenity.OKLabel("Try Again"), zenity.InfoIcon)...) != nil {
				return
			}
			continue
		}
		labels := make([]string, len(disks))
		for i, d := range disks {
			labels[i] = d.Label()
		}
		choice, err := zenity.List("Choose the SD card:", labels,
			append(opts, zenity.OKLabel("Next"), zenity.ExtraButton("Refresh"), zenity.DisallowEmpty())...)
		if errors.Is(err, zenity.ErrExtraButton) {
			continue
		}
		if err != nil {
			return
		}
		for i, l := range labels {
			if l == choice {
				disk = disks[i]
			}
		}
		if disk.ID == "" {
			continue
		}
		if disk.Size < flasher.MinCardSize {
			_ = zenity.Warning(fmt.Sprintf("%s is too small: the device needs a card of 8 GB or more.", disk.Label()), opts...)
			disk = flasher.Disk{}
			continue
		}
		break
	}
	if zenity.Question(fmt.Sprintf("Everything on this disk will be erased:\n\n%s\n\nWrite the Off The Cloud image to it?", disk.Label()),
		append(opts, zenity.OKLabel("Erase"), zenity.CancelLabel("Cancel"), zenity.WarningIcon)...) != nil {
		return
	}

	// 3. Write, as administrator.
	wdlg, err := zenity.Progress(append(opts, zenity.MaxValue(100), zenity.NoCancel())...)
	if err != nil {
		u.setupError(err)
		return
	}
	_ = wdlg.Text("Waiting for permission…")
	err = flasher.WriteElevated(image, disk, func(p flasher.Progress) {
		_ = wdlg.Value(p.Percent())
		_ = wdlg.Text(p.Step())
	})
	_ = wdlg.Close()
	if errors.Is(err, flasher.ErrCancelled) {
		_ = zenity.Info("The card was not written: writing an SD card needs administrator permission. "+
			"Choose Set Up a New Device… again when you are ready (the download is kept).", opts...)
		return
	}
	if err != nil {
		u.setupError(err)
		return
	}

	// 4. The download is no longer needed.
	_ = os.Remove(image)

	// 5. How to continue.
	steps := flasher.NextSteps
	if runtime.GOOS == "windows" {
		steps += "\n\nIf Windows offers to format the card, choose Cancel: Windows can't read the device's Linux partition, and that is expected."
	}
	_ = zenity.Info(steps, append(opts, zenity.Title("Your SD Card Is Ready"))...)
}

func (u *ui) setupError(err error) {
	_ = zenity.Error("The setup stopped:\n\n"+err.Error(), zenity.Title(setupTitle))
}
