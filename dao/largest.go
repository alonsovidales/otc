// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	pb "github.com/alonsovidales/otc/proto/generated"
)

// LargestAfter is where a page of LargestFiles starts: after the content
// of Size bytes and Hash, in the list's order. The zero value, with First
// set, is the first page.
type LargestAfter struct {
	First bool
	Size  int64
	Hash  string
}

// LargestFiles is Settings › Largest files' page (ListLargestFiles in
// messages.proto): up to limit contents, the largest first and, at the
// same size, by hash, after after - each once, however many paths hold it,
// as its row at the first of them in byte order. Every file counts,
// wherever it is (documents, folders kept out of Images); older versions
// don't. The rows come back in the list's order, with where the next page
// starts - after the last content the page read, even one deleted before
// its row was: more is false once the list is over.
//
// There is no index on size (db.sql): the contents are grouped by the
// hash's index and sorted on each page, which is what keeps writing a
// file as cheap as it was.
func (dao *Dao) LargestFiles(after LargestAfter, limit int) (files []*pb.File, next LargestAfter, more bool, err error) {
	query := "select `hash`, max(`size`) as `s` from `files` group by `hash`"
	var args []any
	if !after.First {
		query += " having `s` < ? or (`s` = ? and `hash` > ?)"
		args = append(args, after.Size, after.Size, after.Hash)
	}
	args = append(args, limit)
	rows, err := dao.db.Query(query+" order by `s` desc, `hash` limit ?", args...)
	if err != nil {
		return nil, next, false, err
	}
	var hashes []string
	for rows.Next() {
		if err := rows.Scan(&next.Hash, &next.Size); err != nil {
			rows.Close()
			return nil, LargestAfter{}, false, err
		}
		hashes = append(hashes, next.Hash)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, LargestAfter{}, false, err
	}
	byHash, err := dao.FilesOfHashes(hashes)
	if err != nil {
		return nil, LargestAfter{}, false, err
	}
	files = make([]*pb.File, 0, len(hashes))
	for _, h := range hashes {
		// Gone between the two reads: left out, as a file deleted
		// between pages is.
		if f := byHash[h]; f != nil {
			files = append(files, f)
		}
	}
	return files, next, len(hashes) == limit, nil
}
