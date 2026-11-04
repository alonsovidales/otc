// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A disk or a network share mounted inside a two-way folder (a NAS under
// /home/ana/Sync/nas) is, while it isn't mounted, an empty folder: the
// files it holds look deleted here, and were deleted on the device. The
// folder's own identity (rootid.go) can't tell, being in the folder above.
// So the scan notes the folders below the root that are another file
// system than their parent (scan.mounts, from st_dev; not on Windows,
// where a volume mounted in a folder is a junction the scan never enters),
// the record keeps them (folderMeta.Mounts), and one that is a plain,
// empty folder again holds everything under it, as a folder that could
// not be read: no action either way, the baselines kept, counted in
// "could not be read". One that is gone altogether was removed on purpose
// and is forgotten.

// pathSet is a set of slash-separated relative paths.
type pathSet map[string]bool

func newPathSet(paths []string) pathSet {
	s := make(pathSet, len(paths))
	for _, p := range paths {
		s[p] = true
	}

	return s
}

// covers: rel is one of the set's paths, or inside one.
func (s pathSet) covers(rel string) bool {
	if len(s) == 0 {
		return false
	}
	for p := rel; ; {
		if s[p] {
			return true
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

// goneMounts compares the mounts the record lists (recorded) with those
// this pass found (now), both relative to root as the disk spells them:
// gone are those whose folder is there with nothing mounted on it - what
// is under them is held -, and keep is what the record keeps: now and
// gone, sorted.
func goneMounts(root string, recorded, now []string) (gone, keep []string) {
	have := newPathSet(now)
	for _, m := range recorded {
		if have[m] || !safeRelative(m) {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(m))); err == nil && fi.IsDir() {
			gone = append(gone, m)
		}
	}
	keep = append(append([]string(nil), now...), gone...)
	slices.Sort(keep)
	keep = slices.Compact(keep)

	return gone, keep
}
