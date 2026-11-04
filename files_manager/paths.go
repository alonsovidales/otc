// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"errors"
	"path"
	"strings"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
)

// What a file's path may be. A path reaches the files table only through
// an upload (UploadFile, and FinishUpload after BeginUpload, both through
// registerUpload) or a LinkFile: nothing renames, moves or makes a folder
// on the device - a client moves or copies a file with a LinkFile of its
// content and a DelFile, and a folder is only the paths of its files.
// Shared galleries, share archives and posts only name files that exist.
// checkNewPath is the one rule all of them go by.

// validNewPath reports whether a new file may be stored at p: an absolute
// path ("/" first) of a file (not ending in "/"), none of whose names is
// empty, "." or "..", with no NUL. The apps and the sync clients only make
// such paths (a cleaned local path, a folder and a name); anything else
// came from a client gone wrong - and a ".." in a path went into a share
// archive's entry names as it was.
func validNewPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return false
	}
	for _, name := range strings.Split(p[1:], "/") {
		if name == "" || name == "." || name == ".." {
			return false
		}
	}
	return true
}

// BadPathError is the refusal of a path no new file may have
// (validNewPath).
type BadPathError struct{ Path string }

func (e *BadPathError) msg() i18n.Msg { return i18n.DevFilesBadPath(e.Path) }

func (e *BadPathError) Error() string { return alerttext.EnglishOf(e.msg()) }

// Localized is the refusal in lang (devtext.Text, for the reply).
func (e *BadPathError) Localized(lang string) string {
	if devtext.English(lang) {
		return e.Error()
	}
	return e.msg().Render(lang)
}

// checkNewPath refuses (BadPathError) a path a new file may not have,
// unless a file is at that very path already: a row stored before paths
// were checked stays its client's to replace, read and delete.
func (mg *Manager) checkNewPath(p string) error {
	if validNewPath(p) {
		return nil
	}
	if mg.dao != nil {
		if _, err := mg.dao.GetFileByPath(p); err == nil {
			return nil
		}
	}
	return &BadPathError{Path: p}
}

// SpacesNameError is an upload's or a link's refusal when its path is
// taken by a file whose name differs from it only by spaces at its end
// ("notes.txt" and "notes.txt "). The database keeps the two as one key
// (dao.exactPath), so the second can't be stored: it used to replace the
// first one's content (a sync client's edit), or be refused as a
// duplicate on every pass.
type SpacesNameError struct{ Path string }

func (e *SpacesNameError) msg() i18n.Msg { return i18n.DevFilesNameDiffersBySpaces(path.Base(e.Path)) }

func (e *SpacesNameError) Error() string { return alerttext.EnglishOf(e.msg()) }

// Localized is the refusal in lang (devtext.Text, for the reply).
func (e *SpacesNameError) Localized(lang string) string {
	if devtext.English(lang) {
		return e.Error()
	}
	return e.msg().Render(lang)
}

// takenByAnotherName is what an insert at p refused as duplicated says
// when no row is at p itself, byte for byte (err, GetFileByPath's
// sql.ErrNoRows): a SpacesNameError when the row the key found has
// another name; err itself when there is none (deleted in between).
func (mg *Manager) takenByAnotherName(p string, err error) error {
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if stored, serr := mg.dao.SamePathAs(p); serr == nil && stored != p {
		return &SpacesNameError{Path: p}
	}
	return err
}

// zipEntryName is a share archive's name for the file at p, the folder the
// share's files have in common (prefix) taken off: its names joined by
// "/", leaving out any that is empty, "." or ".." - a path stored before
// paths were checked could hold one, and an unzipper that doesn't check
// writes "../" outside the folder it unpacks to - and with a "\" made
// "_", which unzippers on Windows take for a folder's end.
func zipEntryName(p, prefix string) string {
	var names []string
	for _, name := range strings.Split(strings.TrimPrefix(p, prefix), "/") {
		if name == "" || name == "." || name == ".." {
			continue
		}
		names = append(names, strings.ReplaceAll(name, `\`, "_"))
	}
	if len(names) == 0 {
		return "file"
	}
	return strings.Join(names, "/")
}
