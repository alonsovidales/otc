// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

// Issue #201: a photo stored at several paths. Images lists it once, at its
// oldest copy (SearchMedia), says on each entry how many paths hold it
// (File.copies, CountCopies), and can delete every one of them
// (DelFile.every_copy, PathsOfHash).

// CountCopies is, for each of hashes, how many paths in Files hold it: its
// rows in `files`, wherever they are (folders kept out of Images included).
// Older versions (file_versions) aren't paths and don't count. A hash no
// row holds any more is left out. cHashChunk at a time, each an index
// lookup.
func (dao *Dao) CountCopies(hashes []string) (map[string]int32, error) {
	counts := make(map[string]int32, len(hashes))
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		rows, err := dao.db.Query("select `hash`, count(*) from `files` where `hash` in ("+ph+") group by `hash`", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			var n int32
			if err := rows.Scan(&h, &n); err != nil {
				rows.Close()
				return nil, err
			}
			counts[h] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return counts, nil
}

// PathsOfHash is every path in Files that holds hash (its `files` rows),
// by path.
func (dao *Dao) PathsOfHash(hash string) (paths []string, err error) {
	rows, err := dao.db.Query("select `path` from `files` where `hash` = ? order by `path`", hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
