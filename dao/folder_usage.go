// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"strings"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #195: Files' Storage view (GetFolderUsage in messages.proto) - what
// each direct child of a folder holds, every file under it at any depth.
//
// One statement reads each table once, by the same index range on `path`
// as a listing (underPrefix), and the database only cuts each row's path
// down to the child it belongs to; the sums are made here. Measured on
// 2026-10-14 against the Lima VM's MariaDB 11.8 (128 MB buffer pool, as
// devices run; warm cache) with BenchmarkFolderUsageMySQL's library - 100k
// files: 45k photos in one folder, 50k documents three or four folders
// deep, 5k more, a 250-file folder - and 10k older versions:
//   - the root: 22 ms (summed by a GROUP BY instead: 86 ms - MariaDB copies
//     the 768-character group key into its temporary table for every row);
//     listing the root (ListFiles) takes 114 ms;
//   - a folder holding half the files: 20 ms (56 ms grouped);
//   - the 45k-file folder: 22 ms (169 ms grouped: 45k groups spill to disk);
//   - the 250-file folder: 0.6 ms; a folder of 7 subfolders: 1.8 ms.
// A Pi 5 is about 3-4 times slower than the VM: under 0.1 s at the root.
// The answer has one entry per child.

// FolderUsage is the usage of every direct child of folder (with its
// trailing slash, "/" for the root) that holds a file or an older version,
// in no particular order: its full path (a folder's without the slash,
// as ListFiles lists it), whether it is a folder, the bytes and count of
// its current files and the bytes of the older versions kept of them
// (file_versions, issue #132). A file and a folder of the same name are
// two entries.
func (dao *Dao) FolderUsage(folder string) ([]*pb.FolderUsageEntry, error) {
	q, args := folderUsageQuery(folder)
	rows, err := dao.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		path  string
		isDir bool
	}
	byChild := map[key]*pb.FolderUsageEntry{}
	var entries []*pb.FolderUsageEntry
	for rows.Next() {
		var k key
		var size int64
		var version bool
		if err := rows.Scan(&k.path, &k.isDir, &size, &version); err != nil {
			return nil, err
		}
		e := byChild[k]
		if e == nil {
			e = &pb.FolderUsageEntry{Path: k.path, IsDir: k.isDir}
			byChild[k] = e
			entries = append(entries, e)
		}
		if version {
			e.VersionsSize += size
		} else {
			e.FilesSize += size
			e.FileCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return entries, nil
}

// folderUsageQuery is FolderUsage's statement: for every file and every
// older version under folder, the child it belongs to - the path up to the
// slash after the folder's name: "/a/b/c.txt" in "/a/" is "/a/b", a folder
// because the path goes on, and "/a/c.txt" is itself, a file - its size,
// and whether it is a version.
func folderUsageQuery(folder string) (string, []any) {
	depth := strings.Count(folder, "/") + 1
	cond, condArgs := underPrefix("`path`", folder, false)
	child := "substring_index(`path`, '/', ?), `path` <> substring_index(`path`, '/', ?)"
	q := "select " + child + ", `size`, 0 from `files` where " + cond +
		" union all select " + child + ", `size`, 1 from `file_versions` where " + cond
	args := append([]any{depth, depth}, condArgs...)
	args = append(append(args, depth, depth), condArgs...)

	return q, args
}
