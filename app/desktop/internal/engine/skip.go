// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// What is never synced, either way - two-way folders and backups, files
// here and entries in the device's listings alike. The same list as the
// Mac's (SyncPaths.isExcludedFromSync, SyncPaths.skippedNames), written
// down in CLAUDE.md ("Sync invariants"):
//
//   - hidden: any part of the path below the folder starting with "."
//     (.DS_Store, .git/...); here on Windows also a file or folder with the
//     Hidden or System attribute ($RECYCLE.BIN, System Volume Information),
//     as the Mac leaves out what Finder hides;
//   - this client's partial downloads (".otc-part");
//   - the scratch files of Office and of Windows' Explorer: "~$..." (an
//     open document's owner file, which Word and Excel make and delete as
//     they please), desktop.ini, Thumbs.db, ehthumbs.db and
//     ehthumbs_vista.db (any case).
var skippedNames = []string{"desktop.ini", "thumbs.db", "ehthumbs.db", "ehthumbs_vista.db"}

// skippedName: a file name on the list above.
func skippedName(name string) bool {
	if strings.HasPrefix(name, "~$") {
		return true
	}
	lower := strings.ToLower(name)
	for _, n := range skippedNames {
		if lower == n {
			return true
		}
	}

	return false
}

// notSynced: paths this client never syncs (the list above). rel is
// relative to the folder, so a folder that itself sits under a dot
// directory is still synced.
func notSynced(rel string) bool {
	return isHidden(rel) || strings.HasSuffix(rel, ".otc-part") || skippedName(baseName(rel))
}

func isHidden(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}

	return false
}

// hiddenHere: what the system hides, by more than its name - on Windows
// the Hidden or System attribute (skip_windows.go); nothing elsewhere,
// where a hidden file is one whose name starts with a dot.
func hiddenHere(_ string, fi os.FileInfo) bool { return fi != nil && hiddenAttr(fi) }
