// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// WriteFileAtomic writes data to path through a temporary file of its own
// in the same folder - os.CreateTemp, so two processes (the tray, the
// service, the command line) never write into the same one, as they did
// with a shared "config.json.tmp" - synced to the disk before it is
// renamed over path. A reader sees the old file or the new one, never part
// of one, and a power cut after the rename leaves the new one whole, not
// an empty file. The folder is synced too where that is what makes a
// rename durable (not on Windows, whose file system journals it).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)

		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)

		return err
	}
	// CreateTemp makes it 0600; anything else is asked for explicitly.
	if perm != 0o600 {
		if err := os.Chmod(tmp, perm); err != nil {
			_ = os.Remove(tmp)

			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return err
	}
	SyncDir(dir)

	return nil
}

// SyncDir makes the renames and creations in dir durable, where the system
// needs that asked for (Linux, macOS); best effort.
func SyncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
