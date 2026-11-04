// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// cmdDeleteHeld is the tray's "Delete Them on the Device Too…" on the
// command line: a two-way folder whose deletes the device-side
// mass-deletion guard holds (files gone from it at once, kept on the
// device) gets them deleted there too. It asks first, with the count the
// running sync last saw (--yes doesn't ask), and records the key of that
// set in config.json (the folder's delete_held) for the sync's next pass,
// which runs at once: if the set has changed meanwhile, nothing is deleted
// and the folder shows the new count to be asked again
// (engine/deleteheld.go). The command line itself stays English.
func cmdDeleteHeld(args []string) error { return runDeleteHeld(args, os.Stdin, os.Stdout) }

func runDeleteHeld(args []string, in io.Reader, out io.Writer) error {
	const use = "usage: otc-sync delete-held <id|path> [--yes]"
	yes := false
	var rest []string
	for _, a := range args {
		switch a {
		case "--yes", "-yes", "-y":
			yes = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) != 1 {
		return errors.New(use)
	}
	key := rest[0]
	if abs, err := filepath.Abs(key); err == nil {
		if _, err := os.Stat(abs); err == nil {
			key = abs
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	id, path := "", ""
	for _, f := range cfg.RemoteFolders {
		if f.ID == key || f.LocalPath == key {
			id, path = f.ID, f.LocalPath
		}
	}
	if id == "" {
		for _, f := range cfg.Folders {
			if (f.ID == key || f.Path == key) && f.OneWay {
				return fmt.Errorf("%s is a backup: what is deleted there is never deleted on the device, so nothing is held", f.Path)
			}
		}
		return fmt.Errorf("no two-way folder matches %q (see: otc-sync folders)", rest[0])
	}
	// What the guard holds is only the running sync's to know.
	st := liveState()
	if st == nil {
		return errors.New("the sync is not running, so what it keeps on the device isn't known - start the tray app, or: otc-sync service install")
	}
	var held *config.FolderStatus
	for _, f := range st.RemoteFolders {
		if f.ID == id && f.DeletesHeld > 0 && f.DeletesHeldKey != "" {
			f := f
			held = &f
		}
	}
	if held == nil {
		fmt.Fprintf(out, "Nothing deleted from %s is kept on the device.\n", path)

		return nil
	}
	if !yes {
		fmt.Fprintf(out, "Delete %d files on the device too? They were deleted from %s all at once, so Off The Cloud kept them on the device in case that was a mistake. Deleting them there too removes them everywhere, with their older versions. This can't be undone. Type yes to continue: ",
			held.DeletesHeld, path)
		line, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(line) != "yes" {
			return errors.New("cancelled")
		}
	}
	// The set shown is the one approved: the key, not the count.
	if _, err := config.Edit(func(c *config.Config) (bool, error) {
		return c.SetDeleteHeldRequest(id, held.DeletesHeldKey), nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Deleting the %d files on the device too. The sync does it now; see: otc-sync folders\n", held.DeletesHeld)

	return nil
}

// deleteHeldLabel is a two-way folder's deletes held on the device, after
// its state, with the way to delete them there too; "" when none are.
func deleteHeldLabel(f config.FolderStatus) string {
	if f.DeletesHeld == 0 {
		return ""
	}

	return fmt.Sprintf(" (%d kept on the device - to delete them there too: otc-sync delete-held %s)", f.DeletesHeld, f.ID)
}

// pendingDeleteHeldLabel is a request to delete them recorded in
// config.json, while the sync isn't running.
func pendingDeleteHeldLabel(key string) string {
	if key == "" {
		return ""
	}

	return " (deleting the files kept on the device…)"
}
