// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin

package engine

import (
	"os"
	"path/filepath"
)

// systemTrash moves p to the user's Trash (otc-sync is built for macOS for
// development only; the Mac app itself uses the system's trashItem).
func systemTrash(p string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	trash := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trash, 0o700); err != nil { // perms: rwx------
		return err
	}
	dest, err := freeName(filepath.Join(trash, filepath.Base(p)))
	if err != nil {
		return err
	}

	return os.Rename(p, dest)
}
