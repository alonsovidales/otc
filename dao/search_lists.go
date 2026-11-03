// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"cmp"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #206: the searches BrowseMedia can't read by keyset are read
// through a list of their photos - a hash and what places it in the
// search's order, the photos' own rows read a chunk of hashes at a time as
// the pages need them (SearchMediaOf):
//
//   - A search ranked by its tags' score or by its collection's stars has
//     no index in that order: RankMedia lists its photos by what ranks
//     them, from the tags', faces', collections' and places' tables alone
//     - never `files`, whose rows (kept out, media, before) a chunk applies.
//   - A date-ordered search whose filters keep few photos: BrowseMedia
//     would read most of the library before its first chunk (a person in
//     200 photos of 200,000, or a trip's collection years down), so
//     DatedMedia reads its photos from the smallest filter's own table, by
//     index, each with its oldest copy's date - when an estimate of them
//     (EstimateMedia: a count per filter, by index) is at most a bound.
//   - A text search (files_manager): the photos' cosines to the text.
//
// A chunk is the hashes of its photos in the list's order, taken whole by
// what orders them (every photo of a second, of a score): SearchMediaOf
// reads their rows as SearchMediaRated (SearchMediaNewerThanWithin going
// up) would - the same rows for those hashes, in the same order - so the
// chunks, one after the other, are the whole search's photos.

// ListedHash is a photo in a search's list: its content and what places
// it in the search's order. Rank is, for DatedMedia, its oldest copy's
// date in Unix seconds; for RankMedia its stars and then its tags' score
// (or its tags' score alone). The list's photos that tie on Rank stay
// together in a chunk, so its rows put them in order.
type ListedHash struct {
	Hash string
	Rank [2]float64
}

// cMaxIn is the most hashes one statement asks for; SearchMediaOf reads
// more in several and merges them in the search's order.
const cMaxIn = 1000

// SearchMediaOf is b's search (SearchMediaRated, or going up
// SearchMediaNewerThanWithin; b.Rating may rank) of the photos among
// hashes: each at its oldest copy among the rows b may show, in the
// search's order - for those hashes, the whole search's photos. A hash
// that no row b shows holds any more is left out.
func (dao *Dao) SearchMediaOf(b *MediaBrowse, hashes []string) ([]*pb.File, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	m := &b.MediaFilters
	before := b.Before
	if b.NewerThan != nil {
		before = nil
	}
	// Going up, the plain search's rows (SearchMediaNewerThanWithin has no
	// rating); every row of the hashes, ranked as the search ranks them.
	rating := m.rated()
	if b.NewerThan != nil {
		rating = nil
	}
	type ranked struct {
		file         *pb.File
		stars, score float64
	}
	var all []ranked
	for start := 0; start < len(hashes); start += cMaxIn {
		chunk := hashes[start:min(start+cMaxIn, len(hashes))]
		from, where, _, _, args := searchMediaOnlyClauses(m.Path, m.Tags, m.PersonIDs, m.GroupID, b.ImagesOnly, before, b.Excluded, m.Area, rating, chunk)
		cols := "`f`.`hash`, `f`.`mime`, `f`.`created`, `f`.`modified`, `f`.`path`, `f`.`size`"
		if len(m.Tags) > 0 {
			cols += ", `tg`.`score`"
		}
		if rating != nil {
			cols += ", coalesce(`pr`.`stars`, 0)"
		}
		rows, err := dao.db.Query("select "+cols+" "+from+where, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			file := new(pb.File)
			var created, modified time.Time
			var size int64
			var r ranked
			dest := []any{&file.Hash, &file.Mime, &created, &modified, &file.Path, &size}
			if len(m.Tags) > 0 {
				dest = append(dest, &r.score)
			}
			if rating != nil {
				dest = append(dest, &r.stars)
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, err
			}
			file.Created = timestamppb.New(created)
			file.Modified = timestamppb.New(modified)
			SetFileSize(file, size)
			r.file = file
			all = append(all, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	// SearchMediaRated's order, in Go (the same comparisons: DATETIME,
	// and paths byte by byte as utf8mb4_bin compares them): by stars when
	// ranked so, then by score with tags, then newest first and, at one
	// instant, by path backwards - a total order, the path being unique.
	byStars := rating != nil && rating.ByRating
	byScore := len(m.Tags) > 0 && b.NewerThan == nil
	slices.SortFunc(all, func(x, y ranked) int {
		if byStars {
			if c := cmp.Compare(y.stars, x.stars); c != 0 {
				return c
			}
		}
		if byScore {
			if c := cmp.Compare(y.score, x.score); c != 0 {
				return c
			}
		}
		if c := y.file.Created.AsTime().Compare(x.file.Created.AsTime()); c != 0 {
			return c
		}
		return strings.Compare(y.file.Path, x.file.Path)
	})
	files := make([]*pb.File, len(all))
	for i, r := range all {
		files[i] = r.file
	}
	files = oldestCopies(files)
	if b.NewerThan != nil {
		files = newerThanOldestFirst(files, *b.NewerThan, len(m.Tags) > 0)
	}
	return files, nil
}

// ListOrder is how a list's chunk puts its rows in order.
type ListOrder int

const (
	// ListDated: DatedMedia's list; its chunks are whole seconds, which
	// SearchMediaOf sorts by path.
	ListDated ListOrder = iota
	// ListRanked: RankMedia's list; by its Rank, then newest first and,
	// at one instant, by path backwards (SearchMediaRated's order, by the
	// scores summed once, for the whole list).
	ListRanked
	// ListText: a text search's (files_manager): by Rank[0] (the cosine),
	// best first, ties in the search's own order.
	ListText
)

// MediaListChunk is the photos of list[at:] for b: at least want of them
// (fewer only when the list ends), every photo that ties with the last one
// on Rank included, in the list's order (order) - SearchMediaOf's rows for
// their hashes; a hash b no longer shows is passed over. next is where
// the next chunk starts: len(list) once it is all read.
func (dao *Dao) MediaListChunk(b *MediaBrowse, list []ListedHash, at, want int, order ListOrder) (files []*pb.File, next int, err error) {
	want = max(want, 1)
	for at < len(list) && len(files) < want {
		end := min(at+min(want-len(files), cMaxIn), len(list))
		for end < len(list) && list[end].Rank == list[end-1].Rank {
			end++
		}
		hashes := make([]string, end-at)
		for i, h := range list[at:end] {
			hashes[i] = h.Hash
		}
		rows, err := dao.SearchMediaOf(b, hashes)
		if err != nil {
			return nil, at, err
		}
		if order != ListDated && len(rows) > 1 {
			rank := make(map[string][2]float64, end-at)
			for _, h := range list[at:end] {
				rank[h.Hash] = h.Rank
			}
			if order == ListText {
				slices.SortStableFunc(rows, func(x, y *pb.File) int {
					return cmp.Compare(rank[y.Hash][0], rank[x.Hash][0])
				})
			} else {
				slices.SortFunc(rows, func(x, y *pb.File) int {
					rx, ry := rank[x.Hash], rank[y.Hash]
					if c := cmp.Compare(ry[0], rx[0]); c != 0 {
						return c
					}
					if c := cmp.Compare(ry[1], rx[1]); c != 0 {
						return c
					}
					if c := y.Created.AsTime().Compare(x.Created.AsTime()); c != 0 {
						return c
					}
					return strings.Compare(y.Path, x.Path)
				})
			}
		}
		files = append(files, rows...)
		at = end
	}
	return files, at, nil
}

// mediaDriver is the filter a DatedMedia list is read from: the one an
// estimate says keeps the fewest photos.
type mediaDriver struct {
	// kind is the filter's: "path", "tags", "person", "group", "area" or
	// "none" (people no photo can have all of).
	kind string
	// table is a derived table of its contents' hashes, each once.
	table string
	args  []any
}

// EstimateMedia is at most how many photos m's filters keep, from counts
// by index (an area's by a pass over the positions, small rows), and the
// filter to read them from: the smallest. -1 for no filter.
func (dao *Dao) EstimateMedia(m *MediaFilters) (int, error) {
	n, _, err := dao.estimateMedia(m)
	return n, err
}

func (dao *Dao) estimateMedia(m *MediaFilters) (int, *mediaDriver, error) {
	best, driver := -1, (*mediaDriver)(nil)
	try := func(count string, args []any, d *mediaDriver) error {
		var n int
		if err := dao.db.QueryRow(count, args...).Scan(&n); err != nil {
			return err
		}
		if best < 0 || n < best {
			best, driver = n, d
		}
		return nil
	}
	if m.Path != "" {
		cond, args := underPrefix("`path`", m.Path, true)
		if err := try("select count(*) from `files` where "+cond, args,
			&mediaDriver{kind: "path", table: "(select distinct `hash` from `files` where " + cond + ")", args: args}); err != nil {
			return 0, nil, err
		}
	}
	if len(m.Tags) > 0 {
		ph, args := inPlaceholders(m.Tags)
		if err := try("select count(*) from `file_tags` where `tag` in ("+ph+")", args,
			&mediaDriver{kind: "tags", table: "(select distinct `hash` from `file_tags` where `tag` in (" + ph + "))", args: args}); err != nil {
			return 0, nil, err
		}
	}
	if len(m.PersonIDs) > 0 {
		if !peopleMatchable(m.PersonIDs) {
			return 0, &mediaDriver{kind: "none", table: "(select `hash` from `faces` where 0 = 1)"}, nil
		}
		for _, id := range m.PersonIDs {
			args := []any{id}
			if err := try("select count(*) from `faces` where `person_id` = ?", args,
				&mediaDriver{kind: "person", table: "(select distinct `hash` from `faces` where `person_id` = ?)", args: args}); err != nil {
				return 0, nil, err
			}
		}
	}
	if m.GroupID != "" {
		args := []any{m.GroupID}
		if err := try("select count(*) from `image_group_files` where `group_id` = ?", args,
			&mediaDriver{kind: "group", table: "(select `hash` from `image_group_files` where `group_id` = ?)", args: args}); err != nil {
			return 0, nil, err
		}
	}
	if m.Area != nil {
		cond, args := m.Area.condition("`xl`")
		if err := try("select count(*) from `photo_locations` as `xl` where "+cond, args,
			&mediaDriver{kind: "area", table: "(select `hash` from `photo_locations` as `xl` where " + cond + ")", args: args}); err != nil {
			return 0, nil, err
		}
	}
	return best, driver, nil
}

// DatedMedia is b's photos (a date-ordered search, BrowseMedia's whole),
// each with its oldest copy's date, in the search's order, read from the
// filter that keeps the fewest - when EstimateMedia says that is at most
// limit; ok false (and nothing read) when it is more, or b has no filter.
// The rows are read by index from that filter's table, every row of each
// photo the search may show, and each photo's oldest copy is picked here.
func (dao *Dao) DatedMedia(b *MediaBrowse, limit int) (list []ListedHash, ok bool, err error) {
	n, driver, err := dao.estimateMedia(&b.MediaFilters)
	if err != nil || n < 0 || n > limit {
		return nil, false, err
	}
	hashes, err := dao.datedRows(b, driver)
	if err != nil {
		return nil, false, err
	}
	return hashes, true, nil
}

func (dao *Dao) datedRows(b *MediaBrowse, driver *mediaDriver) ([]ListedHash, error) {
	up := b.NewerThan != nil
	joins, conds, args := b.rowClauses()
	query := "select straight_join `f`.`hash`, `f`.`created` from "
	var queryArgs []any
	if driver.kind != "path" {
		query += driver.table + " as `d` join `files` as `f` on `f`.`hash` = `d`.`hash`"
		queryArgs = append(queryArgs, driver.args...)
	} else {
		// A folder's rows, by the path index, as rowClauses keeps them.
		query += "`files` as `f`"
	}
	query += joins
	if len(conds) > 0 {
		query += " where " + strings.Join(conds, " and ")
	}
	rows, err := dao.db.Query(query, append(queryArgs, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Each photo's oldest copy's second: the list needs no path, as a
	// chunk takes every photo of its seconds and SearchMediaOf sorts them.
	oldest := map[string]int64{}
	var hash sql.RawBytes
	var at time.Time
	for rows.Next() {
		if err := rows.Scan(&hash, &at); err != nil {
			return nil, err
		}
		sec := at.Unix()
		if o, seen := oldest[string(hash)]; !seen || sec < o {
			oldest[string(hash)] = sec
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	list := make([]ListedHash, 0, len(oldest))
	cutoff := int64(0)
	if up {
		cutoff = searchCutoff(*b.NewerThan).Unix()
	}
	for hash, sec := range oldest {
		// Going up, by the oldest copy's date (issue #194), never per row.
		if up && sec <= cutoff {
			continue
		}
		list = append(list, ListedHash{Hash: hash, Rank: [2]float64{float64(sec)}})
	}
	slices.SortFunc(list, func(x, y ListedHash) int {
		c := cmp.Compare(x.Rank[0], y.Rank[0])
		if c == 0 {
			c = strings.Compare(x.Hash, y.Hash)
		}
		if up {
			return c
		}
		return -c
	})
	return list, nil
}

// RankMedia is the photos of a ranked search - by its tags' score, or
// by its collection's stars (and then its tags' score) - by hash, best
// first, with what ranks them (ListedHash.Rank): every content the filters
// keep, from the tags', faces', collections', ratings' and places' tables
// alone, the rows (kept out, media, before, the folder's) left to
// SearchMediaOf. Read from the collection's members when ranked by stars;
// else from the tags' rows (summed per hash as SearchMediaRated sums them),
// or from the filter that keeps fewer contents than the tags
// (EstimateMedia), each one's score then summed by the (hash, tag) index:
// a tag on 80,000 photos and a person in 20,000 read 20,000 hashes, never
// both tables whole - the order the optimizer took on tables without
// statistics. The other filters are lookups per hash, as BrowseMedia's.
// A sum of three tags or more may differ from SearchMediaRated's in its
// last bit (the order it is added in); the list is in its own sums'.
func (dao *Dao) RankMedia(m *MediaFilters) ([]ListedHash, error) {
	rating := m.rated()
	byStars := rating != nil && rating.ByRating
	if !byStars && len(m.Tags) == 0 {
		return nil, fmt.Errorf("not a ranked search") // i18n-ignore: a caller's mistake, never shown
	}
	tagPh, tagArgs := inPlaceholders(m.Tags)
	var selectArgs, fromArgs, joinArgs, whereArgs []any
	var from, score, joins string
	var where []string
	scoreOf := func() string {
		selectArgs = append(selectArgs, tagArgs...)
		return "(select sum(`xs`.`score`) from `file_tags` as `xs` where `xs`.`hash` = `d`.`hash` and `xs`.`tag` in (" + tagPh + "))"
	}
	if byStars {
		from = "`image_group_files` as `d`"
		where = append(where, "`d`.`group_id` = ?")
		whereArgs = append(whereArgs, m.GroupID)
		joins = " left join `photo_ratings` as `pr` on `pr`.`group_id` = `d`.`group_id` and `pr`.`hash` = `d`.`hash`"
		if len(m.Tags) > 0 {
			score = scoreOf()
		}
	} else {
		ranked := *m
		ranked.Path = ""
		_, driver, err := dao.estimateMedia(&ranked)
		if err != nil {
			return nil, err
		}
		if driver.kind == "tags" {
			from = "(select `hash`, sum(`score`) as `score` from `file_tags` where `tag` in (" + tagPh + ") group by `hash`) as `d`"
			fromArgs = tagArgs
			score = "`d`.`score`"
		} else {
			from = driver.table + " as `d`"
			fromArgs = driver.args
			score = scoreOf()
		}
		if m.GroupID != "" {
			joins += " join `image_group_files` as `ig` on `ig`.`group_id` = ? and `ig`.`hash` = `d`.`hash`"
			joinArgs = append(joinArgs, m.GroupID)
			if rating != nil {
				joins += " left join `photo_ratings` as `pr` on `pr`.`group_id` = `ig`.`group_id` and `pr`.`hash` = `d`.`hash`"
			}
		}
	}
	if m.Area != nil {
		cond, areaArgs := m.Area.condition("`pl`")
		joins += " join `photo_locations` as `pl` on `pl`.`hash` = `d`.`hash` and " + cond
		joinArgs = append(joinArgs, areaArgs...)
	}
	if len(m.PersonIDs) > 0 {
		if !peopleMatchable(m.PersonIDs) {
			where = append(where, "0 = 1")
		} else {
			for _, id := range m.PersonIDs {
				where = append(where, "(select 1 from `faces` as `xc` where `xc`.`hash` = `d`.`hash` and `xc`.`person_id` = ? limit 1) is not null")
				whereArgs = append(whereArgs, id)
			}
		}
	}
	if rating != nil {
		where = append(where, rating.conditions()...)
	}
	cols := "`d`.`hash`"
	if byStars {
		cols += ", coalesce(`pr`.`stars`, 0)"
	}
	if score != "" {
		cols += ", " + score + " as `score`"
	}
	query := "select straight_join " + cols + " from " + from + joins
	if len(where) > 0 {
		query += " where " + strings.Join(where, " and ")
	}
	if score != "" {
		// A content with none of the tags has no sum: not the search's.
		query += " having `score` is not null"
	}
	args := append(append(append(selectArgs, fromArgs...), joinArgs...), whereArgs...)
	rows, err := dao.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ListedHash
	var hash sql.RawBytes
	var h ListedHash
	dest := []any{&hash, &h.Rank[0]}
	if byStars && score != "" {
		dest = append(dest, &h.Rank[1])
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		h.Hash = string(hash)
		list = append(list, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(x, y ListedHash) int {
		if c := cmp.Compare(y.Rank[0], x.Rank[0]); c != 0 {
			return c
		}
		if c := cmp.Compare(y.Rank[1], x.Rank[1]); c != 0 {
			return c
		}
		return strings.Compare(x.Hash, y.Hash)
	})
	return list, nil
}
