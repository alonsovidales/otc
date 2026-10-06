// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import pb "github.com/alonsovidales/otc/proto/generated"

// SetFileSize gives f the size n in both of its fields (issue #187):
// size64 holds it, and the int32 size keeps what it has always carried, n
// wrapped to int32, for the apps already in the stores. Every pb.File the
// device fills in goes through here, so the two never disagree.
func SetFileSize(f *pb.File, n int64) {
	f.Size64 = n
	f.Size = int32(n)
}

// FileSize is f's size in bytes: size64, or size from a device before
// release 93, which leaves size64 at 0.
func FileSize(f *pb.File) int64 {
	if f.Size64 != 0 {
		return f.Size64
	}
	return int64(f.Size)
}

// SizeColumnsWide is whether the size columns of files, file_versions and
// social_publications_files are BIGINT (release 93): until they are, a
// size of 2 GiB or more can't be stored.
func (dao *Dao) SizeColumnsWide() (bool, error) {
	var n int
	err := dao.db.QueryRow("select count(*) from information_schema.columns where table_schema = database() and column_name = 'size' " +
		"and table_name in ('files', 'file_versions', 'social_publications_files') and data_type = 'bigint'").Scan(&n)
	return n == 3, err
}

// SizesBackfilled is whether the size backfill (issue #187) has run on
// this database.
func (dao *Dao) SizesBackfilled() (done bool, err error) {
	err = dao.db.QueryRow("select `sizes_backfilled` from `settings`").Scan(&done)
	return
}

func (dao *Dao) SetSizesBackfilled() error {
	_, err := dao.db.Exec("update `settings` set `sizes_backfilled` = 1")
	return err
}

// ContentHashesAfter is the next limit of the hashes the files and kept
// versions use, in order, after after ("" for the first page).
func (dao *Dao) ContentHashesAfter(after string, limit int) (hashes []string, err error) {
	rows, err := dao.db.Query("select `hash` from `files` where `hash` > ? union select `hash` from `file_versions` where `hash` > ? order by `hash` limit ?",
		after, after, limit)
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

// SetSizeOfHash gives every file and kept version with this content the
// size n, and says how many rows it changed.
func (dao *Dao) SetSizeOfHash(hash string, n int64) (changed int64, err error) {
	for _, table := range []string{"files", "file_versions"} {
		res, err := dao.db.Exec("update `"+table+"` set `size` = ? where `hash` = ? and `size` <> ?", n, hash, n)
		if err != nil {
			return changed, err
		}
		rows, _ := res.RowsAffected()
		changed += rows
	}
	return changed, nil
}

// OwnPublicationHashes is every hash the owner's own posts use.
func (dao *Dao) OwnPublicationHashes() (hashes []string, err error) {
	rows, err := dao.db.Query("select distinct f.`hash` from `social_publications_files` f " +
		"join `social_publications` p on p.`uuid` = f.`uuid` where p.`own_publication` = 1")
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

// SetOwnPublicationSize gives the files with this hash in the owner's own
// posts the size n, and says how many rows it changed. Friends' posts
// keep what their files take on this disk (see social.storeFriendFile).
func (dao *Dao) SetOwnPublicationSize(hash string, n int64) (int64, error) {
	res, err := dao.db.Exec("update `social_publications_files` f join `social_publications` p on p.`uuid` = f.`uuid` "+
		"set f.`size` = ? where f.`hash` = ? and p.`own_publication` = 1 and f.`size` <> ?", n, hash, n)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
