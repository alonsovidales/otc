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

// DelFacesByHash removes every face found in the content hash, in one
// transaction, and returns how many went. A person whose cover was one of
// them gets none (ListPeople then shows their oldest remaining face); an
// unnamed person left with no face at all goes too. A named one stays:
// the name is the owner's, and the person can still be deleted by hand.
// Content with no faces - nearly always - costs one indexed query.
func (dao *Dao) DelFacesByHash(hash string) (n int64, err error) {
	people, err := dao.facePeople(hash)
	if err != nil || len(people) == 0 {
		return 0, err
	}

	tx, err := dao.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err = tx.Exec("update `people` set `cover_face_id` = null where `cover_face_id` in (select `id` from `faces` where `hash` = ?)", hash); err != nil {
		return 0, fmt.Errorf("clearing covers: %w", err)
	}
	res, err := tx.Exec("delete from `faces` where `hash` = ?", hash)
	if err != nil {
		return 0, fmt.Errorf("deleting faces: %w", err)
	}
	if n, err = res.RowsAffected(); err != nil {
		return 0, err
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(people)), ",")
	args := make([]any, len(people))
	for i, id := range people {
		args[i] = id
	}
	if _, err = tx.Exec("delete from `people` where `id` in ("+ph+") and `name` = '' and not exists (select 1 from `faces` where `faces`.`person_id` = `people`.`id`)", args...); err != nil {
		return 0, fmt.Errorf("deleting people left with no face: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// facePeople is the people with a face in the content hash.
func (dao *Dao) facePeople(hash string) (ids []string, err error) {
	rows, err := dao.db.Query("select distinct `person_id` from `faces` where `hash` = ?", hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
