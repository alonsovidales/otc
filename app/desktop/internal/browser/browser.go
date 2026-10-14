// SPDX-License-Identifier: AGPL-3.0-or-later

// Package browser opens a web address in the user's default browser - what
// NSWorkspace.open does for the macOS app's "Open Web App" (issue #193) -
// or a folder on this computer in the system's file manager, as a click on
// a folder in the macOS app's panel does (issue #197).
package browser

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
)

// Open opens addr, which must be an http or https address: nothing else
// is handed to the desktop's opener.
func Open(addr string) error {
	u, err := url.Parse(addr)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("not a web address: %q", addr) // i18n-ignore: the command line's: the tray only opens the http(s) address it made
	}

	return open(u.String())
}

// OpenFolder shows the folder at path in the system's file manager: File
// Explorer on Windows, the desktop's own on Linux (xdg-open). path must be
// absolute and an existing folder. One that is gone - or is now a file,
// which the shell would run - fails with an error wrapping fs.ErrNotExist
// before anything is launched; the Mac's FolderOpener says "missing" for
// the same two.
func OpenFolder(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("not a folder on this computer: %q", path) // i18n-ignore: never shown: the tray only opens the absolute paths in its config
	}
	info, err := os.Stat(path)
	if err != nil {
		return err // a missing one wraps fs.ErrNotExist
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder: %w", path, fs.ErrNotExist) // i18n-ignore: the tray words fs.ErrNotExist itself (desk.main.folder_missing)
	}

	return launchFolder(filepath.Clean(path))
}

// launchFolder hands a checked folder to the desktop (browser_*.go); the
// tests replace it.
var launchFolder = openFolder
