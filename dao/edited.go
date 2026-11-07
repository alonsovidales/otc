// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Photo edits (files_manager/edit_image.go, db.sql's edited_content): an
// edit is content of its own, and edited_content names the photo it was
// made from. That original has no path; it is kept - its blob, thumbnails,
// tags and faces - while an edit of it is held, which is what
// HashReferenced counts it by.

// EditedContent is an edited_content row: Hash, made from Original (whose
// mime and size these are). OriginalExt is the original's name's
// extension as it was (".HEIC") when the edit changed it, else "".
type EditedContent struct {
	Hash         string
	Original     string
	OriginalMime string
	OriginalSize int64
	OriginalExt  string
}

// ErrNoEdits is the answer of the edit writes on a database without
// edited_content (a binary newer than its schema): nothing was changed.
var ErrNoEdits = errors.New("this database keeps no photo edits") // i18n-ignore: a sentinel for errors.Is; the reply says not_editable

// EditOf is the edit row of hash, nil when hash isn't an edit (or the
// database has no edits).
func (dao *Dao) EditOf(hash string) (*EditedContent, error) {
	e := &EditedContent{Hash: hash}
	err := dao.db.QueryRow("select `original_hash`, `original_mime`, `original_size`, `original_ext` from `edited_content` where `hash` = ?", hash).
		Scan(&e.Original, &e.OriginalMime, &e.OriginalSize, &e.OriginalExt)
	if errors.Is(err, sql.ErrNoRows) || isNoSuchTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// OriginalsOf is, of hashes, the ones that are edits, each with its
// original (File.original_hash in a listing). Empty on a database without
// edits.
func (dao *Dao) OriginalsOf(hashes []string) (map[string]string, error) {
	out := map[string]string{}
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		rows, err := dao.db.Query("select `hash`, `original_hash` from `edited_content` where `hash` in ("+ph+")", args...)
		if isNoSuchTable(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h, o string
			if err := rows.Scan(&h, &o); err != nil {
				rows.Close()
				return nil, err
			}
			out[h] = o
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// HeldOriginal is an edit's original, by its type and size.
type HeldOriginal struct {
	Mime string
	Size int64
}

// HeldOriginals is, of hashes, the ones kept as the original of an edit:
// the device holds them whole though no path has them (HasFile,
// HasHashes, LinkFile). Empty on a database without edits.
func (dao *Dao) HeldOriginals(hashes []string) (map[string]HeldOriginal, error) {
	out := map[string]HeldOriginal{}
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		rows, err := dao.db.Query("select `original_hash`, `original_mime`, `original_size` from `edited_content` where `original_hash` in ("+ph+")", args...)
		if isNoSuchTable(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			var o HeldOriginal
			if err := rows.Scan(&h, &o.Mime, &o.Size); err != nil {
				rows.Close()
				return nil, err
			}
			out[h] = o
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rowQuerier is a *sql.DB or a *sql.Tx.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// editRefs is how many edits keep hash as their original (in q: the
// database, or a transaction); 0 on a database without edits.
func editRefs(q rowQuerier, hash string) (int, error) {
	var n int
	err := q.QueryRow("select count(*) from `edited_content` where `original_hash` = ?", hash).Scan(&n)
	if isNoSuchTable(err) {
		return 0, nil
	}
	return n, err
}

// EditedFolderFile is the file of folder (with its trailing slash; a
// direct child of it) holding an edit of content, when no file there holds
// content itself (the photo next to its edited copy is the photo) - or of
// which content is the edit, at stem (a path without its extension) with
// another extension, the edit having changed ext into it: the file a
// phone's sync means when it sends the photo it took into the folder
// holding its edit (see files_manager.editedAlready). nil when there is
// none.
func (dao *Dao) EditedFolderFile(folder, content, stem, ext string) (*pb.File, error) {
	cond, args := underPrefix("`f`.`path`", folder, true)
	held, heldArgs := underPrefix("`g`.`path`", folder, true)
	args = append(args, content, content)
	args = append(args, heldArgs...)
	args = append(args, content, likeEscape(stem)+".%")
	rows, err := dao.db.Query("select `f`.`hash`, `f`.`mime`, `f`.`created`, `f`.`modified`, `f`.`path`, `f`.`size`, `e`.`original_hash`, `e`.`original_ext` "+
		"from `files` as `f` join `edited_content` as `e` on `e`.`hash` = `f`.`hash` where "+cond+
		" and ((`e`.`original_hash` = ? and not exists (select 1 from `files` as `g` where `g`.`hash` = ? and "+held+"))"+
		" or (`e`.`hash` = ? and `e`.`original_ext` <> '' and `f`.`path` like ? collate utf8mb4_bin)) order by `f`.`path`", args...)
	if isNoSuchTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var renamed *pb.File
	for rows.Next() {
		var created, modified time.Time
		var size int64
		var original, originalExt string
		f := new(pb.File)
		if err := rows.Scan(&f.Hash, &f.Mime, &created, &modified, &f.Path, &size, &original, &originalExt); err != nil {
			return nil, err
		}
		f.Created, f.Modified = timestamppb.New(created), timestamppb.New(modified)
		SetFileSize(f, size)
		f.OriginalHash = original
		if original == content {
			return f, nil
		}
		if renamed == nil && originalExt == ext {
			renamed = f
		}
	}
	return renamed, rows.Err()
}

// EditTarget is one path an edit or a revert changes: Path, holding From
// until then, to have To. Names is its new name's candidates, the first
// free one taken (nil keeps the path).
type EditTarget struct {
	Path  string
	Names func(n int) string
}

// cNameTries is how many of a target's names an edit tries before it
// gives up on that path.
const cNameTries = 100

// ApplyEdit gives each target holding from the content to (its hash, mime
// and size), in one transaction: another target given other content (or
// deleted) meanwhile is left out, and when the first one was, nothing is
// changed and the answer is ErrOtherContent. edit, when set, is recorded
// for to (an edit's row: it is kept if to already has one). The rows as
// they are after it come back, in the order of targets, the first
// target's first. When no file holds from after it, from's collections,
// favourite and ratings go to to (moved, not copied; a collection already
// holding to keeps its own).
func (dao *Dao) ApplyEdit(targets []EditTarget, from string, to *pb.File, edit *EditedContent) (done []*pb.File, err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if edit != nil {
		if _, err = tx.Exec("insert ignore into `edited_content` (`hash`, `original_hash`, `original_mime`, `original_size`, `original_ext`, `edited`) values (?, ?, ?, ?, ?, ?)",
			edit.Hash, edit.Original, edit.OriginalMime, edit.OriginalSize, edit.OriginalExt, time.Now().UTC()); err != nil {
			if isNoSuchTable(err) {
				return nil, ErrNoEdits
			}
			return nil, err
		}
	}
	now := time.Now().UTC()
	for i, t := range targets {
		var hash string
		var created time.Time
		err = tx.QueryRow("select `hash`, `created` from `files` where "+exactPath("`path`")+" for update", t.Path, t.Path).Scan(&hash, &created)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && hash != from) {
			if i == 0 {
				return nil, ErrOtherContent
			}
			continue // deleted, or given other content, meanwhile
		}
		if err != nil {
			return nil, err
		}
		path, err := renameInTx(tx, t, func(newPath string) error {
			_, err := tx.Exec("update `files` set `hash` = ?, `mime` = ?, `size` = ?, `modified` = ?, `path` = ? where "+exactPath("`path`"),
				to.Hash, to.Mime, FileSize(to), now, newPath, t.Path, t.Path)
			return err
		})
		if err != nil {
			return nil, err
		}
		f := &pb.File{Hash: to.Hash, Mime: to.Mime, Path: path, Created: timestamppb.New(created), Modified: timestamppb.New(now)}
		SetFileSize(f, FileSize(to))
		done = append(done, f)
	}
	if len(done) > 0 {
		if err = moveMembershipsIfGone(tx, from, to.Hash); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if len(done) > 0 {
		dao.markMonths(from, to.Hash)
	}
	return done, nil
}

// renameInTx runs update with the target's first name that is free (its
// own path when it has no names), and answers the path it took. A taken
// name is a duplicate key, which leaves the transaction as it was.
func renameInTx(tx *sql.Tx, t EditTarget, update func(newPath string) error) (string, error) {
	if t.Names == nil {
		return t.Path, update(t.Path)
	}
	for n := 0; n < cNameTries; n++ {
		name := t.Names(n)
		err := update(name)
		if isDuplicateKey(err) {
			continue
		}
		return name, err
	}
	return t.Path, update(t.Path)
}

// AddEditCopy stores file - an edit's copy, next to the photo it was made
// from - at the first of names that is free, and records edit (nil: the
// copy is no edit), in one transaction. The row as stored comes back.
func (dao *Dao) AddEditCopy(file *pb.File, names func(n int) string, edit *EditedContent) (*pb.File, error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if edit != nil {
		if _, err = tx.Exec("insert ignore into `edited_content` (`hash`, `original_hash`, `original_mime`, `original_size`, `original_ext`, `edited`) values (?, ?, ?, ?, ?, ?)",
			edit.Hash, edit.Original, edit.OriginalMime, edit.OriginalSize, edit.OriginalExt, time.Now().UTC()); err != nil {
			if isNoSuchTable(err) {
				return nil, ErrNoEdits
			}
			return nil, err
		}
	}
	stored := proto.Clone(file).(*pb.File)
	for n := 0; ; n++ {
		if n == cNameTries {
			return nil, errors.New("no free name for the edit's copy") // i18n-ignore: wrapped by the caller's translated message
		}
		stored.Path = names(n)
		_, err = tx.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`, `cloud_id`) values (?, ?, ?, ?, ?, ?, NULL)",
			stored.Hash, stored.Mime, stored.Created.AsTime(), stored.Modified.AsTime(), stored.Path, FileSize(stored))
		if isDuplicateKey(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		break
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	dao.markMonths(stored.Hash)
	return stored, nil
}

// moveMembershipsIfGone moves from's collections (Favourites among them)
// and ratings to to, inside tx, when no file holds from any more: the photo
// people put there is the edit now. A collection that holds both keeps
// to's own, and from's leftovers there go.
func moveMembershipsIfGone(tx *sql.Tx, from, to string) error {
	var n int
	if err := tx.QueryRow("select count(*) from `files` where `hash` = ?", from).Scan(&n); err != nil {
		return err
	}
	if n > 0 || from == to {
		return nil
	}
	for _, table := range []string{"image_group_files", "photo_ratings", "photo_rating_sources"} {
		if _, err := tx.Exec("update ignore `"+table+"` set `hash` = ? where `hash` = ?", to, from); err != nil {
			if isNoSuchTable(err) {
				continue // a database from before ratings
			}
			return err
		}
		if _, err := tx.Exec("delete from `"+table+"` where `hash` = ?", from); err != nil {
			return err
		}
	}
	return nil
}

// DelEdit forgets that hash is an edit (it is gone) and answers what it was
// made from, "" when it wasn't one.
func (dao *Dao) DelEdit(hash string) (original string, err error) {
	e, err := dao.EditOf(hash)
	if err != nil || e == nil {
		return "", err
	}
	if _, err = dao.db.Exec("delete from `edited_content` where `hash` = ?", hash); err != nil {
		return "", err
	}
	return e.Original, nil
}

// FaceBox is a face's box, in the pixels of the photo shown upright.
type FaceBox struct{ X, Y, W, H int }

// CopyFaces gives to the faces of from, each with its box moved by move
// (false: it is not in to - cropped out), each to the same person, in one
// transaction: an edit keeps who is in it. Nothing when to has faces
// already. Answers how many were given.
func (dao *Dao) CopyFaces(from, to string, move func(FaceBox) (FaceBox, bool), newID func() string) (int, error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var has int
	if err = tx.QueryRow("select count(*) from `faces` where `hash` = ?", to).Scan(&has); err != nil {
		return 0, err
	}
	if has > 0 {
		return 0, nil
	}
	rows, err := tx.Query("select `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `quality` from `faces` where `hash` = ? order by `created`, `id`", from)
	if err != nil {
		return 0, err
	}
	type face struct {
		person    string
		box       FaceBox
		embedding []byte
		thumbnail []byte
		quality   sql.NullFloat64
	}
	var faces []face
	for rows.Next() {
		var f face
		if err := rows.Scan(&f.person, &f.box.X, &f.box.Y, &f.box.W, &f.box.H, &f.embedding, &f.thumbnail, &f.quality); err != nil {
			rows.Close()
			return 0, err
		}
		faces = append(faces, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range faces {
		box, kept := move(f.box)
		if !kept {
			continue
		}
		if _, err = tx.Exec("insert into `faces` (`id`, `hash`, `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `created`, `quality`) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			newID(), to, f.person, box.X, box.Y, box.W, box.H, f.embedding, f.thumbnail, time.Now().UTC(), f.quality); err != nil {
			return 0, err
		}
		n++
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}
