// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
)

// faces rows are keyed by content hash, like tags and thumbnails, and go
// with the content's last file or kept version. They used to stay: a
// deleted photo's face crops and embeddings (biometric data) were kept
// for good, still counted and shown as covers in People, and still
// matched against new photos. The people added to the content by hand
// (people_by_hand.go) go with them.

// DeletedFace is a face DelFacesByHash removed, and the person it was
// matched to.
type DeletedFace struct {
	ID       string
	PersonID string
}

// DelFacesByHash removes every face found in the content hash, and every
// person added to it by hand, in one transaction, and returns the faces.
// A person whose cover was one of them gets none (ListPeople then shows
// their oldest remaining face); an unnamed person left with no face, and
// added by hand to nothing, goes too. A named one stays: the name is the
// owner's, and the person can still be deleted by hand. Content with no
// faces - nearly always - costs one indexed query.
func (dao *Dao) DelFacesByHash(hash string) (faces []DeletedFace, err error) {
	faces, byHand, err := dao.contentPeople(hash)
	if err != nil || (len(faces) == 0 && len(byHand) == 0) {
		return nil, err
	}
	var people []string
	seen := map[string]bool{}
	for _, id := range append(facePeople(faces), byHand...) {
		if !seen[id] {
			seen[id] = true
			people = append(people, id)
		}
	}

	tx, err := dao.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if len(faces) > 0 {
		if _, err = tx.Exec("update `people` set `cover_face_id` = null, `cover_chosen` = (`cover_image` is not null) where `cover_face_id` in (select `id` from `faces` where `hash` = ?)", hash); err != nil {
			return nil, fmt.Errorf("clearing covers: %w", err)
		}
		if _, err = tx.Exec("delete from `faces` where `hash` = ?", hash); err != nil {
			return nil, fmt.Errorf("deleting faces: %w", err)
		}
	}
	if len(byHand) > 0 {
		if _, err = execIfByHand(tx, "delete from `people_by_hand` where `hash` = ?", hash); err != nil {
			return nil, fmt.Errorf("deleting the people added by hand: %w", err)
		}
	}
	ph, args := inPlaceholders(people)
	if _, err = byHandExec(tx, "delete from `people` where `id` in ("+ph+")"+cNoOtherRefs, args...); err != nil {
		return nil, fmt.Errorf("deleting people left with no face: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return faces, nil
}

// DelFacesByHashes is DelFacesByHash for many contents at once, in one
// transaction and cHashChunk hashes per statement (issue #192: a folder
// kept out of Images loses the faces of everything only it holds, and the
// people added to it by hand). The same rules: covers among them are
// cleared, unnamed people left with no face (nor added by hand anywhere)
// go, named ones stay.
func (dao *Dao) DelFacesByHashes(hashes []string) (faces []DeletedFace, err error) {
	withFaces, withByHand := map[string]bool{}, map[string]bool{}
	var people []string
	seen := map[string]bool{}
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		// A face's id is never '': the rows with none are people added by
		// hand.
		rows, err := dao.byHandQuery("select `id`, `person_id`, `hash` from `faces` where `hash` in ("+ph+")"+
			" union all select '', `b`.`person_id`, `b`.`hash` from "+cByHand+" as `b` where `b`.`hash` in ("+ph+")", append(args, args...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var f DeletedFace
			var h string
			if err := rows.Scan(&f.ID, &f.PersonID, &h); err != nil {
				rows.Close()
				return nil, err
			}
			if f.ID == "" {
				withByHand[h] = true
			} else {
				faces = append(faces, f)
				withFaces[h] = true
			}
			if !seen[f.PersonID] {
				seen[f.PersonID] = true
				people = append(people, f.PersonID)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(people) == 0 {
		return nil, nil
	}
	var affected, byHand []string
	for _, h := range hashes {
		if withFaces[h] {
			affected = append(affected, h)
			delete(withFaces, h) // once, should hashes repeat one
		}
		if withByHand[h] {
			byHand = append(byHand, h)
			delete(withByHand, h)
		}
	}

	tx, err := dao.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, chunk := range hashChunks(affected) {
		ph, args := inPlaceholders(chunk)
		if _, err = tx.Exec("update `people` set `cover_face_id` = null, `cover_chosen` = (`cover_image` is not null) where `cover_face_id` in (select `id` from `faces` where `hash` in ("+ph+"))", args...); err != nil {
			return nil, fmt.Errorf("clearing covers: %w", err)
		}
		if _, err = tx.Exec("delete from `faces` where `hash` in ("+ph+")", args...); err != nil {
			return nil, fmt.Errorf("deleting faces: %w", err)
		}
	}
	for _, chunk := range hashChunks(byHand) {
		ph, args := inPlaceholders(chunk)
		if _, err = execIfByHand(tx, "delete from `people_by_hand` where `hash` in ("+ph+")", args...); err != nil {
			return nil, fmt.Errorf("deleting the people added by hand: %w", err)
		}
	}
	for _, chunk := range hashChunks(people) {
		ph, args := inPlaceholders(chunk)
		if _, err = byHandExec(tx, "delete from `people` where `id` in ("+ph+")"+cNoOtherRefs, args...); err != nil {
			return nil, fmt.Errorf("deleting people left with no face: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return faces, nil
}

// contentPeople is the faces found in the content hash, with their people,
// and the people added to it by hand, in one query.
func (dao *Dao) contentPeople(hash string) (faces []DeletedFace, byHand []string, err error) {
	rows, err := dao.byHandQuery("select `id`, `person_id` from `faces` where `hash` = ?"+
		" union all select '', `b`.`person_id` from "+cByHand+" as `b` where `b`.`hash` = ?", hash, hash)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f DeletedFace
		if err := rows.Scan(&f.ID, &f.PersonID); err != nil {
			return nil, nil, err
		}
		if f.ID == "" {
			byHand = append(byHand, f.PersonID)
		} else {
			faces = append(faces, f)
		}
	}
	return faces, byHand, rows.Err()
}

// facePeople is the people of faces, in order (repeats kept).
func facePeople(faces []DeletedFace) []string {
	out := make([]string, len(faces))
	for i, f := range faces {
		out[i] = f.PersonID
	}
	return out
}

// OrphanFaceHashes is the content hashes that have faces, or people added
// by hand, but no file or kept version any more, nor are kept as an
// edit's original - left by deletes from before faces went with their
// content.
func (dao *Dao) OrphanFaceHashes() (hashes []string, err error) {
	const gone = " where not exists (select 1 from `files` where `files`.`hash` = `fc`.`hash`) and not exists (select 1 from `file_versions` where `file_versions`.`hash` = `fc`.`hash`)"
	const kept = " and not exists (select 1 from `edited_content` where `edited_content`.`original_hash` = `fc`.`hash`)"
	orphan := func(edits string) string {
		return "select `hash` from `faces` as `fc`" + gone + edits + " union select `hash` from " + cByHand + " as `fc`" + gone + edits
	}
	rows, err := dao.byHandQuery(orphan(kept))
	if isNoSuchTable(err) {
		rows, err = dao.byHandQuery(orphan("")) // a database without edits
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}

// HashHasFaces is whether faces were already found in the content hash.
// A content's faces are found once: detecting them again added a second
// set of rows (new ids, no unique key), inflating People - see
// files_manager.processFaces. People added by hand aren't faces found:
// the content is still analysed. Nor are pets' (faces.kind, release 180):
// whether a content was looked at for pets is pet_scans'.
func (dao *Dao) HashHasFaces(hash string) (bool, error) {
	var one int
	err := dao.db.QueryRow("select 1 from `faces` where `hash` = ? and `kind` = 0 limit 1", hash).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
