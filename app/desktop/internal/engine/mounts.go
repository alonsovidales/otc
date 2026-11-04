// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
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

// A record from before the record kept its mounts (otc-sync 0.18 and
// older: no "root" yet) knows nothing of a disk mounted inside the folder.
// If that disk isn't mounted at the first pass after the update, its files
// look deleted here, and a handful of them is under the mass-deletion
// guard. So at that first pass, a folder here under which the record lists
// files and the scan finds none - still a plain folder, and not changed
// since the record was written, as a mount point that nothing touched
// while its disk was away; a folder emptied by hand since is newer - is
// held like a gone mount (maybeMounts), and the record keeps it
// ("maybe_mounts"). So is a mount point the system declares there (Linux:
// /etc/fstab, systemd mount units), however recently it changed. It is
// held while it stays so; once files are there again (the disk is back, or
// new files) or the record lists none under it any more, it is an ordinary
// folder again.

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

// declaredMounts is declaredMountPoints (a test sets its own).
var declaredMounts = declaredMountPoints

// maybeMounts are the folders (keys) to hold as maybe a disk or share
// mounted in root that isn't now: of the candidates - those the record
// keeps (meta.MaybeMounts), or at the first pass of a record from before
// mounts were kept, the emptiedFolders and the mount points the system
// declares inside root (declared.go) - those still a plain folder here
// with nothing found under them (byRel) while record lists files under
// them. Sorted.
func maybeMounts(root string, meta folderMeta, record, byRel map[string]string, known func(string) bool, written time.Time) []string {
	candidates := meta.MaybeMounts
	if meta.Root == "" && len(meta.Mounts) == 0 {
		candidates = append(emptiedFolders(root, record, byRel, known, written), declaredUnder(root, declaredMounts())...)
	}
	if len(candidates) == 0 {
		return nil
	}
	hereUnder, recordUnder := pathSet{}, pathSet{}
	for key := range byRel {
		for d := parentKey(key); d != "" && !hereUnder[d]; d = parentKey(d) {
			hereUnder[d] = true
		}
	}
	for key, entry := range record {
		if strings.HasPrefix(entry, keptOnDevicePrefix) {
			continue
		}
		for d := parentKey(key); d != "" && !recordUnder[d]; d = parentKey(d) {
			recordUnder[d] = true
		}
	}
	var out []string
	for _, m := range candidates {
		if !safeRelative(m) || hereUnder[m] || !recordUnder[m] {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(m))); err == nil && fi.IsDir() {
			out = append(out, m)
		}
	}
	slices.Sort(out)

	return slices.Compact(out)
}

// emptiedFolders: the folders below root, each the shallowest of its
// branch, under which the record lists files (plain entries, not known
// otherwise - unreadable, skipped) and the scan found none (byRel), that
// are a plain folder here not changed since written, the time the record
// file was last written.
func emptiedFolders(root string, record, byRel map[string]string, known func(string) bool, written time.Time) []string {
	if written.IsZero() {
		return nil
	}
	localDirs := map[string]bool{}
	for key := range byRel {
		for d := parentKey(key); d != "" && !localDirs[d]; d = parentKey(d) {
			localDirs[d] = true
		}
	}
	checked, found := map[string]bool{}, map[string]bool{}
	for key, entry := range record {
		if strings.HasPrefix(entry, keptOnDevicePrefix) || (known != nil && known(key)) {
			continue
		}
		if _, here := byRel[key]; here {
			continue
		}
		for i := strings.IndexByte(key, '/'); i >= 0; i = nextSlash(key, i) {
			d := key[:i]
			if localDirs[d] {
				continue // files here under it: look deeper
			}
			if !checked[d] {
				checked[d] = true
				if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(d))); err == nil && fi.IsDir() && fi.ModTime().Before(written) {
					found[d] = true
				}
			}

			break
		}
	}
	out := make([]string, 0, len(found))
	for d := range found {
		out = append(out, d)
	}
	slices.Sort(out)

	return out
}

// nextSlash is the index of the next "/" in key after i, or -1.
func nextSlash(key string, i int) int {
	if j := strings.IndexByte(key[i+1:], '/'); j >= 0 {
		return i + 1 + j
	}

	return -1
}
