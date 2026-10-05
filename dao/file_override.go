// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// OverrideFile makes file the current content of its path in place, in one
// transaction, and returns the hash it replaced ("" when the path had no
// row any more: it is then inserted, as StoreNewFile would). The path's
// kept versions (issue #132) are left alone: overriding a file is not
// deleting it. The override used to be a DelFile and a new insert, which
// destroyed every version of the path - a folder that was once upload
// only and later unlocked lost them all at the desktop app's next edit -
// and left the path with no row for a moment, so another client's listing
// saw it deleted.
//
// The replaced content's tags go as a delete's would (DelFileByPathHash)
// when this row was its last use; its blob is the caller's to remove.
// cloud_id is set to the new content's, or none: the old one names the
// old content.
func (dao *Dao) OverrideFile(file *pb.File, cloudID string) (oldHash string, err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	cloud := sql.NullString{String: cloudID, Valid: cloudID != ""}
	err = tx.QueryRow("select `hash` from `files` where `path` = ? for update", file.Path).Scan(&oldHash)
	if err == sql.ErrNoRows {
		if _, err = tx.Exec(
			"insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`, `cloud_id`) values (?, ?, ?, ?, ?, ?, ?)",
			file.Hash, file.Mime, file.Created.AsTime(), file.Modified.AsTime(), file.Path, file.Size, cloud); err != nil {
			return "", err
		}
		return "", tx.Commit()
	}
	if err != nil {
		return "", err
	}

	if oldHash != file.Hash {
		var refCount, versionRefs int
		if err = tx.QueryRow("select count(*) from `files` where `hash` = ? for update", oldHash).Scan(&refCount); err != nil {
			return "", err
		}
		if err = tx.QueryRow("select count(*) from `file_versions` where `hash` = ?", oldHash).Scan(&versionRefs); err != nil {
			return "", err
		}
		if refCount+versionRefs <= 1 {
			if _, err = tx.Exec("delete from `file_tags` where `hash` = ?", oldHash); err != nil {
				return "", err
			}
		}
	}
	if _, err = tx.Exec("update `files` set `hash` = ?, `mime` = ?, `size` = ?, `created` = ?, `modified` = ?, `cloud_id` = ? where `path` = ?",
		file.Hash, file.Mime, file.Size, file.Created.AsTime(), file.Modified.AsTime(), cloud, file.Path); err != nil {
		return "", err
	}

	return oldHash, tx.Commit()
}
