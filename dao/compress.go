// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Compress (files_manager/compress.go and compress_job.go, release 177): a
// photo or video re-encoded smaller in place of every copy of it, saved as
// an edit is (ReplaceEveryCopy), after which the original's bytes are
// deleted and its edits' rows say so (original_removed). What the options
// and the job ask of the database first, a few hundred files at a time, is
// here.

// CompressSupported is whether this database records a removed original
// (edited_content.original_removed, release 177's 177.sh): without it
// nothing is compressed, as an original could not be marked deleted.
func (dao *Dao) CompressSupported() (bool, error) {
	rows, err := dao.db.Query("select `original_removed` from `edited_content` limit 0")
	if isUnknownColumn(err) || isNoSuchTable(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rows.Close()
	return true, nil
}

// PathsOfHashes is PathsOfHash for many hashes, cHashChunk to a query: each
// hash's paths in Files, by path. A hash no row holds is left out.
func (dao *Dao) PathsOfHashes(hashes []string) (map[string][]string, error) {
	out := make(map[string][]string, len(hashes))
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		rows, err := dao.db.Query("select `hash`, `path` from `files` where `hash` in ("+ph+") order by `hash`, `path`", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h, p string
			if err := rows.Scan(&h, &p); err != nil {
				rows.Close()
				return nil, err
			}
			out[h] = append(out[h], p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// HashesInVersions is, of hashes, those an older version keeps
// (file_versions): their bytes stay whatever the files do.
func (dao *Dao) HashesInVersions(hashes []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		rows, err := dao.db.Query("select distinct `hash` from `file_versions` where `hash` in ("+ph+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			out[h] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ContentHeld is whether a file or a kept version holds hash - what
// HashReferenced counts but an edit's row naming it as its original: the
// check before a compressed file's original is deleted, which those rows
// always name.
func (dao *Dao) ContentHeld(hash string) (bool, error) {
	var n int
	err := dao.db.QueryRow("select (select count(*) from `files` where `hash` = ?) + (select count(*) from `file_versions` where `hash` = ?)", hash, hash).Scan(&n)
	return n > 0, err
}

// SetOriginalRemoved records on every edit of original whether its bytes
// were deleted: once they are, none of them can be reverted; when a
// compress keeps them after all (a copy was linked meanwhile), its edit is
// an ordinary one.
func (dao *Dao) SetOriginalRemoved(original string, removed bool) error {
	_, err := dao.db.Exec("update `edited_content` set `original_removed` = ? where `original_hash` = ?", removed, original)
	return schemaBehind(err)
}

// RemovedOriginals is every original a compress deleted, or marked to be
// deleted (the start's sweep finishes one a restart interrupted). Empty on
// a database without the column.
func (dao *Dao) RemovedOriginals() ([]string, error) {
	rows, err := dao.db.Query("select distinct `original_hash` from `edited_content` where `original_removed` = 1")
	if isUnknownColumn(err) || isNoSuchTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// RemovedOriginalEdit is what the device holds for original when a
// compress deleted its bytes: the content of its newest edit whose row
// says so (an edit of the compressed file, made later, is newer), at its
// first path in Files - or, when none is, as an older version keeps it.
// nil when original isn't such an original: never compressed, or a file
// holds it itself (its bytes are there). One query, by the edits' index
// on original_hash: it is asked for every upload of new content.
func (dao *Dao) RemovedOriginalEdit(original string) (*pb.File, error) {
	const pick = "select `f`.`hash`, `f`.`mime`, `f`.`created`, `f`.`modified`, `f`.`path`, `f`.`size`, `e`.`edited`, %d as `k` from %s as `f` join `edited_content` as `e` on `e`.`hash` = `f`.`hash`" +
		" where `e`.`original_hash` = ? and `e`.`original_removed` = 1 and not exists (select 1 from `files` as `g` where `g`.`hash` = ?)"
	f := new(pb.File)
	var created, modified, edited time.Time
	var size int64
	var kind int
	err := dao.db.QueryRow(fmt.Sprintf(pick, 0, "`files`")+" union all "+fmt.Sprintf(pick, 1, "`file_versions`")+" order by `k`, `edited` desc, `path` limit 1",
		original, original, original, original).
		Scan(&f.Hash, &f.Mime, &created, &modified, &f.Path, &size, &edited, &kind)
	if errors.Is(err, sql.ErrNoRows) || isUnknownColumn(err) || isNoSuchTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.Created, f.Modified = timestamppb.New(created), timestamppb.New(modified)
	SetFileSize(f, size)
	f.OriginalHash, f.OriginalRemoved = original, true
	return f, nil
}
