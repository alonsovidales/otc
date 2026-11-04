// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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

		return nil
	}

	return fmt.Errorf("no free name in %s", dir) // i18n-ignore: a file's error, only logged
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
