// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A file a two-way pass deletes here - deleted on the device (from the
// web, a phone, another computer) - goes to the system's trash, as the Mac
// app moves it to the Trash: os.Remove made it unrecoverable, and on the
// device the delete has already taken its versions and, with the last
// copy, its content. The trash is the freedesktop.org one on Linux (the
// home trash for files on its disk, the disk's own .Trash-<uid> for the
// others; trash_xdg.go), the Recycle Bin on Windows (trash_windows.go),
// the user's ~/.Trash on macOS (development builds only; trash_darwin.go).
// Where none takes the file - a network share, a removable disk Windows
// has no Recycle Bin for, a disk the user can't write a trash folder at the
// top of - it goes to a .otc-trash folder in the synced folder (dot-named,
// so never synced), in a folder per day, with its path below the synced
// folder; days older than folderTrashDays are emptied after the passes.
// Never deleted outright: a file that can't be moved anywhere stays, and
// its delete is tried again at the next pass.

const (
	folderTrashName = ".otc-trash"
	folderTrashDays = 30
)

// trashSystem is systemTrash (the tests on Windows keep the Recycle Bin
// out of it).
var trashSystem = systemTrash

// moveToTrash moves p, a file in the two-way folder root, to the system's
// trash, or else to root's own (folderTrash).
func moveToTrash(p, root string) error {
	err := trashSystem(p)
	if err == nil {
		return nil
	}
	noteTrashFallback(root, err)

	return folderTrash(p, root, time.Now())
}

// trashFallbacks: the folders whose deletes went to their own trash, logged
// once each.
var (
	trashFallbackMu sync.Mutex
	trashFallbacks  = map[string]bool{}
)

func noteTrashFallback(root string, why error) {
	trashFallbackMu.Lock()
	first := !trashFallbacks[root]
	trashFallbacks[root] = true
	trashFallbackMu.Unlock()
	if first {
		log.Printf("the system's trash can't take files from %s (%v) - what is deleted there goes to %s in it, for %d days", root, why, folderTrashName, folderTrashDays)
	}
}

// folderTrash moves p into root's trash folder: <top>/.otc-trash/<day>/<the
// path of p below top>, where top is root - or, for a file on a disk or
// share mounted inside root, that file system's top folder, as a rename
// can't leave its file system. A name taken there gets a number.
func folderTrash(p, root string, now time.Time) error {
	top := trashTop(p, root)
	rel, err := filepath.Rel(top, p)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s is not inside %s", p, top) // i18n-ignore: a file's error, only logged
	}
	trash := filepath.Join(top, folderTrashName)
	dest := filepath.Join(trash, now.Format("2006-01-02"), rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil { // perms: rwx------
		return err
	}
	hideFile(trash)
	dest, err = freeName(dest)
	if err != nil {
		return err
	}

	return os.Rename(p, dest)
}

// trashTop is the folder whose .otc-trash takes p: root, or the top folder
// below root of the file system p is on when that isn't root's.
func trashTop(p, root string) string {
	root = filepath.Clean(root)
	fi, err := os.Lstat(p)
	if err != nil {
		return root
	}
	dev, ok := fileSystemOf(fi)
	if !ok {
		return root
	}
	top := filepath.Dir(p)
	for top != root {
		parent := filepath.Dir(top)
		pfi, err := os.Stat(parent)
		if err != nil || parent == top {
			break
		}
		if pdev, ok := fileSystemOf(pfi); !ok || pdev != dev {
			break
		}
		top = parent
	}
	if rel, err := filepath.Rel(root, top); err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return root
	}

	return top
}

// recycleBinTakes: Windows' Recycle Bin keeps a file of size bytes rather
// than deleting it for good - which the shell does without a word, with
// the no-confirmation flag otc-sync's silent delete needs, for a file
// larger than the bin holds and for every file where the bin is turned off
// ("Don't move files to the Recycle Bin", the NoRecycleFiles policy). The
// bin's size is maxMB megabytes when Windows keeps one for the volume
// (known); else Windows' default is taken at its smallest, 5% of the
// volume's total bytes. A file is kept when it is within 90% of it.
func recycleBinTakes(size int64, off bool, maxMB uint64, known bool, volTotal uint64) bool {
	if off || size < 0 {
		return false
	}
	limit := volTotal / 20
	if known {
		limit = maxMB << 20
	}

	return limit > 0 && uint64(size) <= limit/10*9
}

// freeName is p, or p with a number before its extension ("a 2.txt") when
// something is at p already.
func freeName(p string) (string, error) {
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	dir, base := filepath.Split(p)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	for i := 2; i < 10000; i++ {
		c := filepath.Join(dir, fmt.Sprintf("%s %d%s", stem, i, ext))
		if _, err := os.Lstat(c); errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
	}

	return "", fmt.Errorf("no free name for %s", base) // i18n-ignore: a file's error, only logged
}

// pruneFolderTrashes prunes every .otc-trash of a two-way folder root: its
// own, and that of each disk or share mounted in it (mounts, relative to
// root as the record keeps them), where folderTrash puts the files that
// were on it.
func pruneFolderTrashes(root string, mounts []string, now time.Time) {
	pruneFolderTrash(root, now)
	for _, m := range mounts {
		if safeRelative(m) {
			pruneFolderTrash(filepath.Join(root, filepath.FromSlash(m)), now)
		}
	}
}

// pruneFolderTrash empties the days in root's .otc-trash older than
// folderTrashDays, and the folder itself once it is empty.
func pruneFolderTrash(root string, now time.Time) {
	trash := filepath.Join(root, folderTrashName)
	entries, err := os.ReadDir(trash)
	if err != nil {
		return
	}
	cutoff := now.AddDate(0, 0, -folderTrashDays)
	left := 0
	for _, en := range entries {
		day, err := time.ParseInLocation("2006-01-02", en.Name(), now.Location())
		if err != nil || !en.IsDir() || !day.Before(cutoff) {
			left++

			continue
		}
		if err := os.RemoveAll(filepath.Join(trash, en.Name())); err != nil {
			log.Printf("could not empty %s/%s: %v", trash, en.Name(), err)
			left++
		}
	}
	if left == 0 {
		_ = os.Remove(trash)
	}
}
