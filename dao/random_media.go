// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import "strings"

// A slideshow's random order (RandomPhotos, the web app's Slideshow): the
// photos of a collection, or of the whole library, each once per round, a
// page at a time. Each content's place is md5(seed + hash) - a fresh
// shuffle for every seed, the same order for every page of one, with no
// list kept anywhere - its hash after it (`k`), which orders the contents
// completely, so a page starts after the previous one's last `k` and a
// round ends once a page comes back short. A content added to the library
// or the collection during a round shows in it if its place is still
// ahead, and one taken out isn't given again.
//
// Each page reads the contents' rows whole (no index holds md5(seed +
// hash)): a `files` scan for the library - its images, outside the
// folders kept out of Images -, the collection's own rows (by its primary
// key) with each content's rows by `hash` for a collection; sorted with a
// limit, so a heap of limit rows, never the whole list. 50,000 photos a
// page every few minutes (a page lasts its photos' seconds on screen).
// SearchMediaOf then reads the rows the page shows, as Images would.

// cRandomKeyLen is the length of a page's last key: md5 (32 hex digits)
// and the hash (64).
const cRandomKeyLen = 32 + 64

// RandomMedia is a round's next page (RandomPhotos): at most limit
// contents of the photos in groupID (the whole library for "") with a
// row outside the folders excluded, by md5(seed + hash), after the key
// after ("" from the start). last is the key to start the next page
// after; done says the round has no more.
func (dao *Dao) RandomMedia(groupID string, excluded []string, seed, after string, limit int) (hashes []string, last string, done bool, err error) {
	if limit <= 0 {
		return nil, "", true, nil
	}
	rows, args := randomRows(groupID, excluded)
	key := "concat(md5(concat(?, `x`.`hash`)), `x`.`hash`)"
	q := "select `hash`, `k` from (select `x`.`hash`, " + key + " as `k` from (" + rows + ") as `x`) as `r`"
	all := append([]any{seed}, args...)
	if after != "" {
		q += " where `k` > ?"
		all = append(all, after)
	}
	q += " order by `k` limit ?"
	all = append(all, limit)
	res, err := dao.db.Query(q, all...)
	if err != nil {
		return nil, "", false, err
	}
	defer res.Close()
	for res.Next() {
		var hash, k string
		if err := res.Scan(&hash, &k); err != nil {
			return nil, "", false, err
		}
		hashes = append(hashes, hash)
		last = k
	}
	if err := res.Err(); err != nil {
		return nil, "", false, err
	}
	if last == "" {
		last = after
	}
	return hashes, last, len(hashes) < limit, nil
}

// randomRows is the statement listing a round's contents (a `hash`
// column), and its args.
func randomRows(groupID string, excluded []string) (string, []any) {
	conds := []string{"`f`.`mime` like 'image%'"}
	var condArgs []any
	if cond, args := outsideFolders("`f`.`path`", excluded); cond != "" {
		conds = append(conds, cond)
		condArgs = append(condArgs, args...)
	}
	if groupID == "" {
		return "select distinct `f`.`hash` from `files` as `f` where " + strings.Join(conds, " and "), condArgs
	}
	// The collection's own rows, each content kept when a row of it is an
	// image Images shows: one look-up by `hash`, stopping at the first.
	rows := "select `ig`.`hash` from `image_group_files` as `ig` where `ig`.`group_id` = ? and " +
		"(select 1 from `files` as `f` where `f`.`hash` = `ig`.`hash` and " + strings.Join(conds, " and ") + " limit 1) is not null"
	return rows, append([]any{groupID}, condArgs...)
}

// RandomMediaCount is how many contents a round of groupID (the whole
// library for "") holds, with a row outside the folders excluded: the
// rows RandomMedia orders, counted once.
func (dao *Dao) RandomMediaCount(groupID string, excluded []string) (int, error) {
	rows, args := randomRows(groupID, excluded)
	var n int
	err := dao.db.QueryRow("select count(*) from ("+rows+") as `x`", args...).Scan(&n)
	return n, err
}

// RandomKeyValid says whether after can be a page's last key (RandomMedia):
// "" or md5 and a hash, in hex.
func RandomKeyValid(after string) bool {
	if after == "" {
		return true
	}
	if len(after) != cRandomKeyLen {
		return false
	}
	for _, c := range after {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
