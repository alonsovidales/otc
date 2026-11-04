// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// freedesktopTrash moves p to the trash of the freedesktop.org Trash
// specification (trash-spec 1.0), which the desktops' file managers show
// and restore from: the home trash ($XDG_DATA_HOME/Trash) for a file on its
// file system, else the trash at the top of the file's own file system -
// $topdir/.Trash/$uid when $topdir/.Trash is a real folder with the sticky
// bit, $topdir/.Trash-$uid otherwise (made if need be). Each file has its
// .trashinfo (where it was, and when), written first, with O_EXCL, to take
// its name.
func freedesktopTrash(p string, now time.Time) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	dev, ok := fileSystemOf(fi)
	if !ok {
		return errors.New("no file system id") // i18n-ignore: a file's error, only logged
	}
	home := filepath.Join(xdgDataHome(), "Trash")
	if hdev, ok := nearestFileSystem(home); ok && hdev == dev {
		if err := ensureTrashDir(home, false); err != nil {
			return err
		}

		return trashInto(home, abs, abs, now)
	}
	top := topDir(abs, dev)
	rel, err := filepath.Rel(top, abs)
	if err != nil {
		return err
	}
	uid := strconv.Itoa(os.Getuid())
	if shared, err := os.Lstat(filepath.Join(top, ".Trash")); err == nil && shared.IsDir() && shared.Mode()&os.ModeSticky != 0 {
		dir := filepath.Join(top, ".Trash", uid)
		if ensureTrashDir(dir, true) == nil && trashInto(dir, abs, rel, now) == nil {
			return nil
		}
	}
	dir := filepath.Join(top, ".Trash-"+uid)
	if err := ensureTrashDir(dir, true); err != nil {
		return err
	}

	return trashInto(dir, abs, rel, now)
}

// xdgDataHome is $XDG_DATA_HOME, or ~/.local/share.
func xdgDataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d
	}
	home, _ := os.UserHomeDir()

	return filepath.Join(home, ".local", "share")
}

// nearestFileSystem is the file system of p, or of its nearest folder that
// exists (the home trash may not be made yet).
func nearestFileSystem(p string) (uint64, bool) {
	for {
		if fi, err := os.Stat(p); err == nil {
			return fileSystemOf(fi)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return 0, false
		}
		p = parent
	}
}

// topDir is the top folder of the file system dev that holds abs: its
// mount point.
func topDir(abs string, dev uint64) string {
	top := filepath.Dir(abs)
	for {
		parent := filepath.Dir(top)
		if parent == top {
			return top
		}
		fi, err := os.Stat(parent)
		if err != nil {
			return top
		}
		if pdev, ok := fileSystemOf(fi); !ok || pdev != dev {
			return top
		}
		top = parent
	}
}

// ensureTrashDir makes dir and its files and info folders. A trash at the
// top of a disk (strict) must be this user's own folder, not a link
// someone put there; the home trash may be a link the user made.
func ensureTrashDir(dir string, strict bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil { // perms: rwx------
		return err
	}
	stat := os.Stat
	if strict {
		stat = os.Lstat
	}
	fi, err := stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", dir) // i18n-ignore: a file's error, only logged
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not this user's", dir) // i18n-ignore: a file's error, only logged
	}
	for _, sub := range []string{"files", "info"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil { // perms: rwx------
			return err
		}
	}

	return nil
}

// trashInto moves abs into the trash dir, under a name of its own, with a
// .trashinfo saying it was at path (absolute for the home trash, relative
// to the top folder for a disk's own).
func trashInto(dir, abs, path string, now time.Time) error {
	files, info := filepath.Join(dir, "files"), filepath.Join(dir, "info")
	base := filepath.Base(abs)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	body := "[Trash Info]\nPath=" + trashPathEscape(path) + "\nDeletionDate=" + now.Format("2006-01-02T15:04:05") + "\n"
	for i := 1; i < 10000; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s.%d%s", stem, i, ext)
		}
		ip := filepath.Join(info, name+".trashinfo")
		fh, err := os.OpenFile(ip, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // perms: rw-------
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, werr := fh.WriteString(body)
		if cerr := fh.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(ip)

			return werr
		}
		dest := filepath.Join(files, name)
		if _, err := os.Lstat(dest); err == nil {
			_ = os.Remove(ip) // a file left there without its info: another name

			continue
		}
		if err := os.Rename(abs, dest); err != nil {
			_ = os.Remove(ip)

			return err
		}
		noteOwnTrash(dir, name, body, now)

		return nil
	}

	return fmt.Errorf("no free name in %s", dir) // i18n-ignore: a file's error, only logged
}

// What otc-sync puts in a freedesktop.org trash is emptied after
// folderTrashDays, as its own .otc-trash is. A computer that runs otc-sync
// as a service, with no desktop, has nobody to empty the trash: every file
// deleted on the device piled up in ~/.local/share/Trash for good, until
// the disk was full and downloads were refused for room. Only its own
// entries go - each listed, as it is trashed, in a file in the config
// folder - and only while still as it left them (its .trashinfo
// unchanged): one restored, emptied or replaced is forgotten.

// ownTrashFile is that list's file name: one JSON line per trashed file.
const ownTrashFile = "trashed.jsonl"

// ownTrashEntry is one file otc-sync put in a trash.
type ownTrashEntry struct {
	Dir  string `json:"dir"`  // the trash (files/, info/)
	Name string `json:"name"` // its name in files/, and in info/ with .trashinfo
	Info string `json:"info"` // the .trashinfo written for it
	At   int64  `json:"at"`   // when, unix seconds
}

var (
	ownTrashMu     sync.Mutex
	ownTrashPruned time.Time // the last pruneOwnTrash, at most hourly
	ownTrashLogged bool      // a failure to list one is logged once
)

func ownTrashPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, ownTrashFile), nil
}

// noteOwnTrash lists a file otc-sync just put in the trash dir; best effort
// (a file not listed stays in the trash, as any).
func noteOwnTrash(dir, name, info string, now time.Time) {
	ownTrashMu.Lock()
	defer ownTrashMu.Unlock()
	err := appendOwnTrash(ownTrashEntry{Dir: dir, Name: name, Info: info, At: now.Unix()})
	if err != nil && !ownTrashLogged {
		ownTrashLogged = true
		log.Printf("could not note what went to the trash (%v) - it stays there until the trash is emptied", err)
	}
}

func appendOwnTrash(en ownTrashEntry) error {
	p, err := ownTrashPath()
	if err != nil {
		return err
	}
	line, err := json.Marshal(en)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) // perms: rw-------
	if err != nil {
		return err
	}
	_, werr := fh.Write(append(line, '\n'))
	if cerr := fh.Close(); werr == nil {
		werr = cerr
	}

	return werr
}

// pruneOwnTrash empties what otc-sync put in a trash more than
// folderTrashDays ago, at most once an hour (after the passes). An entry
// whose trash isn't there now (a disk not mounted) waits for it.
func pruneOwnTrash(now time.Time) {
	ownTrashMu.Lock()
	defer ownTrashMu.Unlock()
	if !ownTrashPruned.IsZero() && now.Sub(ownTrashPruned) < time.Hour && now.After(ownTrashPruned) {
		return
	}
	ownTrashPruned = now
	p, err := ownTrashPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	cutoff := now.AddDate(0, 0, -folderTrashDays).Unix()
	var keep [][]byte
	changed := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var en ownTrashEntry
		if json.Unmarshal(line, &en) != nil || en.Dir == "" || en.Name == "" || strings.ContainsAny(en.Name, "/\x00") {
			changed = true

			continue
		}
		if en.At > cutoff || !emptyOwnTrashEntry(en) {
			keep = append(keep, line)

			continue
		}
		changed = true
	}
	if !changed {
		return
	}
	if len(keep) == 0 {
		_ = os.Remove(p)

		return
	}
	out := append(bytes.Join(keep, []byte("\n")), '\n')
	if err := config.WriteFileAtomic(p, out, 0o600); err != nil { // perms: rw-------
		log.Printf("could not update the list of what went to the trash: %v", err)
	}
}

// emptyOwnTrashEntry removes en's file and .trashinfo from its trash when
// they are still as otc-sync left them, and says whether en is done with:
// removed, or no longer otc-sync's (restored, emptied, replaced). False
// keeps it for later: its trash isn't there now, or the removal failed.
func emptyOwnTrashEntry(en ownTrashEntry) bool {
	info := filepath.Join(en.Dir, "info", en.Name+".trashinfo")
	got, err := os.ReadFile(info)
	if errors.Is(err, os.ErrNotExist) {
		_, derr := os.Stat(filepath.Join(en.Dir, "info"))

		return derr == nil // the trash is there, without it: not ours now
	}
	if err != nil {
		return false
	}
	if string(got) != en.Info {
		return true
	}
	file := filepath.Join(en.Dir, "files", en.Name)
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("could not empty %s from the trash: %v", file, err)

		return false
	}
	_ = os.Remove(info)

	return true
}

// trashPathEscape writes a path as .trashinfo's Path wants it: the bytes
// of the name, escaped as in URLs (RFC 2396), its slashes kept.
func trashPathEscape(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', strings.IndexByte("-_.~/", c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}

	return b.String()
}
