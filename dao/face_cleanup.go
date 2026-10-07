// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"strings"
)

// faces rows are keyed by content hash, like tags and thumbnails, and go
// with the content's last file or kept version. They used to stay: a
// deleted photo's face crops and embeddings (biometric data) were kept
// for good, still counted and shown as covers in People, and still
// matched against new photos.

// DeletedFace is a face DelFacesByHash removed, and the person it was
// matched to.
type DeletedFace struct {
	ID       string
	PersonID string
}

// DelFacesByHash removes every face found in the content hash, in one
// transaction, and returns them. A person whose cover was one of them
// gets none (ListPeople then shows their oldest remaining face); an
// unnamed person left with no face at all goes too. A named one stays:
// the name is the owner's, and the person can still be deleted by hand.
// Content with no faces - nearly always - costs one indexed query.
func (dao *Dao) DelFacesByHash(hash string) (faces []DeletedFace, err error) {
	faces, err = dao.hashFaces(hash)
	if err != nil || len(faces) == 0 {
		return nil, err
	}
	var people []string
	seen := map[string]bool{}
	for _, f := range faces {
		if !seen[f.PersonID] {
			seen[f.PersonID] = true
			people = append(people, f.PersonID)
		}
	}

	tx, err := dao.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err = tx.Exec("update `people` set `cover_face_id` = null where `cover_face_id` in (select `id` from `faces` where `hash` = ?)", hash); err != nil {
		return nil, fmt.Errorf("clearing covers: %w", err)
	}
	if _, err = tx.Exec("delete from `faces` where `hash` = ?", hash); err != nil {
		return nil, fmt.Errorf("deleting faces: %w", err)
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(people)), ",")
	args := make([]any, len(people))
	for i, id := range people {
		args[i] = id
	}
	if _, err = tx.Exec("delete from `people` where `id` in ("+ph+") and `name` = '' and not exists (select 1 from `faces` where `faces`.`person_id` = `people`.`id`)", args...); err != nil {
		return nil, fmt.Errorf("deleting people left with no face: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return faces, nil
}

// DelFacesByHashes is DelFacesByHash for many contents at once, in one
// transaction and cHashChunk hashes per statement (issue #192: a folder
// kept out of Images loses the faces of everything only it holds). The
// same rules: covers among them are cleared, unnamed people left with no
// face go, named ones stay.
func (dao *Dao) DelFacesByHashes(hashes []string) (faces []DeletedFace, err error) {
	withFaces := map[string]bool{}
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		rows, err := dao.db.Query("select `id`, `person_id`, `hash` from `faces` where `hash` in ("+ph+")", args...)
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
			faces = append(faces, f)
			withFaces[h] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(faces) == 0 {
		return nil, nil
	}
	var people, affected []string
	seen := map[string]bool{}
	for _, f := range faces {
		if !seen[f.PersonID] {
			seen[f.PersonID] = true
			people = append(people, f.PersonID)
		}
	}
	for _, h := range hashes {
		if withFaces[h] {
			affected = append(affected, h)
			delete(withFaces, h) // once, should hashes repeat one
		}
	}

	tx, err := dao.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, chunk := range hashChunks(affected) {
		ph, args := inPlaceholders(chunk)
		if _, err = tx.Exec("update `people` set `cover_face_id` = null where `cover_face_id` in (select `id` from `faces` where `hash` in ("+ph+"))", args...); err != nil {
			return nil, fmt.Errorf("clearing covers: %w", err)
		}
		if _, err = tx.Exec("delete from `faces` where `hash` in ("+ph+")", args...); err != nil {
			return nil, fmt.Errorf("deleting faces: %w", err)
		}
	}
	for _, chunk := range hashChunks(people) {
		ph, args := inPlaceholders(chunk)
		if _, err = tx.Exec("delete from `people` where `id` in ("+ph+") and `name` = '' and not exists (select 1 from `faces` where `faces`.`person_id` = `people`.`id`)", args...); err != nil {
			return nil, fmt.Errorf("deleting people left with no face: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return faces, nil
}

// hashFaces is the faces found in the content hash, with their people.
func (dao *Dao) hashFaces(hash string) (faces []DeletedFace, err error) {
	rows, err := dao.db.Query("select `id`, `person_id` from `faces` where `hash` = ?", hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f DeletedFace
		if err := rows.Scan(&f.ID, &f.PersonID); err != nil {
			return nil, err
		}
		faces = append(faces, f)
	}
	return faces, rows.Err()
}

// OrphanFaceHashes is the content hashes that have faces but no file or
// kept version any more - left by deletes from before faces went with
// their content.
func (dao *Dao) OrphanFaceHashes() (hashes []string, err error) {
	rows, err := dao.db.Query("select distinct `hash` from `faces` as `fc` where not exists (select 1 from `files` where `files`.`hash` = `fc`.`hash`) and not exists (select 1 from `file_versions` where `file_versions`.`hash` = `fc`.`hash`)")
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
// files_manager.processFaces.
func (dao *Dao) HashHasFaces(hash string) (bool, error) {
	var one int
	err := dao.db.QueryRow("select 1 from `faces` where `hash` = ? limit 1", hash).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
