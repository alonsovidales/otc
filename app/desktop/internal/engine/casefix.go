// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/cases"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// A folder renamed only in case on the device ("photos" to "Photos", from
// the web or a phone) used to be undone there. On a disk that ignores case
// (NTFS, APFS, FAT) the pass deleted photos/a.jpg here and downloaded
// Photos/a.jpg - into the folder still called "photos", the one name the
// disk has for both. The next pass saw photos/a.jpg as new here (uploaded)
// and Photos/a.jpg as deleted here (deleted on the device): the rename
// reverted, and the device's rows made again without their versions.
//
// So before a pass plans anything, a folder here whose name differs only
// in case from the device's - the device has that folder in one case only,
// and the disk takes the device's spelling for this one - is renamed to
// the device's case, and the folder looked at again. Its files keep their
// records and their cached hashes under the new name, so nothing is sent or
// fetched for it. A disk that tells case apart has the two as two folders,
// and nothing is renamed.
//
// Unless the rename was made here (Explorer, Finder): the record then has
// the folder's files under the device's spelling and none under this one.
// That folder is left as it is, and the pass sends the rename up as any -
// its files uploaded under the new name and deleted under the old, which
// is never held by the old name finding the new one (presentAsNamed).

var foldCase = cases.Fold()

// caseFold is a key as a disk that ignores case compares it.
func caseFold(key string) string { return foldCase.String(key) }

// caseRename is one folder renamed: its key and the key it has now, and
// its full path before and after.
type caseRename struct{ from, to, fromDir, toDir string }

// matchFolderCase renames the folders here (in root, holding files, keyed
// by path in local) whose names are the device's (remote's keys) but for
// case, the shallowest first - one level a call; a caller looks at the
// folder again after any rename. record is the sync record: a folder it
// lists under the device's spelling only was renamed here, and stays. It
// returns what it renamed.
func matchFolderCase(root string, local map[string]string, remote map[string]bool, record map[string]string) []caseRename {
	deviceDirs := map[string]bool{}
	for key := range remote {
		for d := parentKey(key); d != ""; d = parentKey(d) {
			deviceDirs[d] = true
		}
	}
	byFold := map[string][]string{}
	for d := range deviceDirs {
		byFold[caseFold(d)] = append(byFold[caseFold(d)], d)
	}
	// The folders here, as the disk spells them, by key.
	localDirs := map[string]string{}
	for key, p := range local {
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil || rel == "." {
			continue
		}
		for disk, k := filepath.ToSlash(rel), parentKey(key); k != ""; disk, k = parentPath(disk), parentKey(k) {
			localDirs[k] = disk
		}
	}
	// The folders the record has files in.
	recordDirs := map[string]bool{}
	for key := range record {
		for d := parentKey(key); d != "" && !recordDirs[d]; d = parentKey(d) {
			recordDirs[d] = true
		}
	}
	var wrong []string
	depth := -1
	for k := range localDirs {
		if deviceDirs[k] {
			continue
		}
		same := byFold[caseFold(k)]
		// The device's folder, in one case only; and this folder's parent
		// already the device's spelling (shallowest first).
		if len(same) != 1 || parentKey(same[0]) != parentKey(k) {
			continue
		}
		// Synced under the device's spelling, never under this one: the
		// rename is this computer's, and goes up.
		if recordDirs[same[0]] && !recordDirs[k] {
			continue
		}
		n := strings.Count(k, "/")
		switch {
		case depth < 0 || n < depth:
			depth, wrong = n, []string{k}
		case n == depth:
			wrong = append(wrong, k)
		}
	}
	sort.Strings(wrong)
	var done []caseRename
	for _, k := range wrong {
		to := byFold[caseFold(k)][0]
		from := filepath.Join(root, filepath.FromSlash(localDirs[k]))
		dest := filepath.Join(filepath.Dir(from), baseName(to))
		// Only where the disk takes the device's spelling for this very
		// folder: it ignores case, and there is no other folder by it.
		a, errA := os.Lstat(from)
		b, errB := os.Lstat(dest)
		if errA != nil || errB != nil || !a.IsDir() || !os.SameFile(a, b) {
			continue
		}
		if err := os.Rename(from, dest); err != nil {
			log.Printf("could not rename %s to %s as the device has it: %v", from, baseName(to), err)

			continue
		}
		log.Printf("renamed %s to %s, as the device has it", from, baseName(to))
		done = append(done, caseRename{from: k, to: to, fromDir: from, toDir: dest})
	}

	return done
}

// presentAsNamed: something is at rel (a key) below root under exactly that
// name - each component as its folder lists it, compared in NFC - and not
// only under a name the disk takes for it. On a disk that ignores case
// (NTFS, APFS) "a.txt" finds "A.txt": a file renamed only in case here,
// which must not hold up the delete of its old name on the device (the
// device tells the two apart, and kept both for good). Read only when
// something answers to the name: a folder's listing per component.
func presentAsNamed(root, rel string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, err
	}
	dir := root
	for _, part := range strings.Split(rel, "/") {
		name, found, err := listedAs(dir, part)
		if err != nil || !found {
			return false, err
		}
		dir = filepath.Join(dir, name)
	}

	return true, nil
}

// listedAs is the entry of dir whose name is name in NFC, as dir spells it.
func listedAs(dir, name string) (string, bool, error) {
	d, err := os.Open(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}

		return "", false, err
	}
	defer d.Close()
	want := pathKey(name)
	for {
		names, err := d.Readdirnames(256)
		for _, n := range names {
			if n == name || pathKey(n) == want {
				return n, true, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", false, nil
			}

			return "", false, err
		}
	}
}

// parentKey is the folder holding key, "" at the top.
func parentKey(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[:i]
	}

	return ""
}

// parentPath is parentKey for a path as the disk spells it.
func parentPath(p string) string { return parentKey(p) }

// renamedKey is key after the folder renames: under a renamed folder, the
// folder's new name.
func renamedKey(key string, renames []caseRename) string {
	for _, r := range renames {
		if rest, ok := strings.CutPrefix(key, r.from+"/"); ok {
			return r.to + "/" + rest
		}
	}

	return key
}

// renamedRecord is a copy of the record with the renamed folders' entries
// under their new names.
func renamedRecord(last map[string]string, renames []caseRename) map[string]string {
	out := make(map[string]string, len(last))
	for k, v := range last {
		out[k] = v
	}
	for k, v := range last {
		if nk := renamedKey(k, renames); nk != k {
			delete(out, k)
			if _, taken := out[nk]; !taken {
				out[nk] = v
			}
		}
	}

	return out
}

// renameHashCache moves the cached hashes of the renamed folders' files to
// their new paths, so they aren't read again.
func (e *Engine) renameHashCache(f config.RemoteFolder, renames []caseRename) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loadHashCacheLocked(f.ID)
	cache := e.hashCache[f.ID]
	for _, r := range renames {
		prefix := r.fromDir + string(filepath.Separator)
		for p, h := range cache {
			if rest, ok := strings.CutPrefix(p, prefix); ok {
				delete(cache, p)
				cache[filepath.Join(r.toDir, rest)] = h
				e.hashDirty[f.ID] = true
			}
		}
	}
}
