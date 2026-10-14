// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"google.golang.org/protobuf/proto"
)

// Issue #201: a photo stored at several paths - typically downloaded to a
// phone or a computer and synced back - is one entry in Images, at its
// oldest copy (dao.SearchMedia). Each entry says how many paths hold it
// (File.copies, setCopies), and the apps then ask whether deleting it from
// Images deletes only that path or every copy (DelFile.every_copy,
// DelEveryCopy).

// setCopies sets File.copies on every file of a search page: the paths in
// Files holding its content, its own included (never below 1, though the
// page may name a path deleted since its search began). Older versions
// (file_versions) aren't paths and don't count. When the count fails it
// sets none (ImageSearch serves the page without them).
func (mg *Manager) setCopies(page []*pb.File) error {
	if len(page) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(page))
	for _, f := range page {
		hashes = append(hashes, f.Hash)
	}
	counts, err := mg.dao.CountCopies(hashes)
	if err != nil {
		return err
	}
	for _, f := range page {
		f.Copies = proto.Int32(max(counts[f.Hash], 1))
	}
	return nil
}

// CopyUploadOnlyError is DelEveryCopy's refusal (RespEnvelope.error_code
// "copy_upload_only"): one of the copies - maybe the path asked for - is
// in an upload-only folder (issue #132), so nothing was deleted. Folder is
// that upload-only folder, with its trailing slash: the outermost one
// holding the copy, whose lock has to go first (issue #186).
type CopyUploadOnlyError struct {
	Folder string
}

func (e *CopyUploadOnlyError) msg() i18n.Msg {
	return i18n.DevFilesCopyUploadOnly(strings.TrimSuffix(e.Folder, "/"))
}

func (e *CopyUploadOnlyError) Error() string { return e.msg().Render("en") }

// Localized is the refusal in lang (devtext.Text, for the reply).
func (e *CopyUploadOnlyError) Localized(lang string) string { return e.msg().Render(lang) }

// ContentChangedError is DelFile's refusal when the client said which
// content it showed for a file's path (DelFile.hash) and the path holds
// other content now - synced over or edited since (RespEnvelope.error_code
// "changed"). Nothing was deleted: the person refreshes and decides again.
// It matters most with every_copy, which would otherwise delete every copy
// of content the person never saw.
type ContentChangedError struct{}

func (e *ContentChangedError) msg() i18n.Msg { return i18n.DevFilesContentChanged() }

func (e *ContentChangedError) Error() string { return e.msg().Render("en") }

// Localized is the refusal in lang (devtext.Text, for the reply).
func (e *ContentChangedError) Localized(lang string) string { return e.msg().Render(lang) }

// shownElsewhere reports whether a file now holding hash isn't the content
// the client showed for its path (shown, DelFile.hash). An empty shown -
// not sent, or a listing's file whose content is missing (issue #141) -
// checks nothing.
func shownElsewhere(shown, hash string) bool { return shown != "" && shown != hash }

// outermostFolder is the outermost of folders (each with its trailing
// slash) holding the file at path, or "".
func outermostFolder(path string, folders []string) string {
	outer := ""
	for _, f := range folders {
		if strings.HasPrefix(path, f) && (outer == "" || len(f) < len(outer)) {
			outer = f
		}
	}
	return outer
}

// DelEveryCopy deletes every path in Files holding the content of the file
// at path (DelFile.every_copy, issue #201): each with its own kept
// versions and, once nothing uses it, the content, as DelFile does for one
// - wherever it is, folders kept out of Images included (their flags stay,
// as DelFile never clears one). Older versions other paths keep are not
// copies: they stay, and so does what they hold. All or nothing: when a
// copy (the asked path included) is in an upload-only folder, nothing is
// deleted and the answer is a CopyUploadOnlyError naming that folder. Nor
// when shown (DelFile.hash, the content the client showed for path) is set
// and path holds other content now: a ContentChangedError, checked before
// the copies are even looked up. A copy linked after the paths were read
// stays; one deleted meanwhile is skipped. A folder's path (no file there)
// is DelPath's, the flag and shown ignored (issue #116).
func (mg *Manager) DelEveryCopy(session *session.Session, path, shown string) error {
	file, err := mg.dao.GetFileByPath(path)
	if errors.Is(err, sql.ErrNoRows) {
		return mg.DelPath(session, path, shown)
	}
	if err != nil {
		return err
	}
	if shownElsewhere(shown, file.Hash) {
		return &ContentChangedError{}
	}
	paths, err := mg.dao.PathsOfHash(file.Hash)
	if err != nil {
		return err
	}
	folders, err := mg.dao.GetUploadOnlyFolders()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if f := outermostFolder(p, folders); f != "" {
			return &CopyUploadOnlyError{Folder: f}
		}
	}
	log.Debug("Deleting every copy of", file.Hash, "-", len(paths), "paths")
	// Issue #192: what the deleted rows held is checked against the
	// folders kept out of Images once, as DelPath does.
	var touched []string
	defer func() { mg.reconcileAnalysis(session, touched) }()
	for _, p := range paths {
		hashes, err := mg.delFile(p)
		touched = append(touched, hashes...)
		if errors.Is(err, sql.ErrNoRows) && len(hashes) == 0 {
			continue // deleted meanwhile
		}
		if err != nil {
			return err
		}
	}
	return nil
}
