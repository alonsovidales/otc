// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"fmt"
	"slices"
	"strings"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #206: Images' searches are read a chunk at a time instead of
// whole. SearchMedia reads every row of a search, sorted in a filesort,
// and keeps each photo's oldest copy in Go: at 200k photos ~100 MB and
// seconds on a Pi before the first tiles, at every new search. BrowseMedia
// gives the same photos in the same order (each at its oldest copy among
// those the search may show, newest first and, at one instant, by path
// backwards; oldest first going up), a chunk at a time, for every search
// in date order: the plain browse (no tag, person, collection, area, text
// or rating) and any search by people, a collection, an area, a folder or
// a rating's filter - and a tag going up, which is in date order too. A
// search ranked by its tags' score, its stars or a text is read through a
// list of its photos instead (search_lists.go), and so is a date-ordered
// one whose filters keep few photos (DatedMedia), which this would find
// only after reading most of the library.
//
// The key is (`created`, `path`): `path` is unique, so it orders the rows
// completely. No index holds both (`path` alone is InnoDB's whole key
// limit, release 143's script; and with no PRIMARY KEY, MariaDB doesn't
// extend the `created` index by it), but `files` has had one on `created`
// from the start, and `created` is a DATETIME - whole seconds. So a chunk
// is whole seconds, in two statements: the dates of its photos and the one
// after them, read in the index's order and stopping there (`order by
// created ... limit want+1`: no filesort, and the rows passed over - copies
// whose photo is older, documents, photos the filters leave out - are read
// once, there); then every photo from the first of those seconds to the
// last, sorted in SQL by (`created`, `path`) - a filesort of a chunk, never
// of the library. The next chunk starts after the last second. A second
// holding very many rows (a sync that stamped them all alike) makes its
// chunk as large; at worst that is the whole library, as before.
//
// Both statements read `files` first (straight_join) and look each row's
// filters up by an index: a collection, its ratings and an area are joins
// on their unique keys (one row or none per photo), and tags and people
// lookups that stop at the first row (a scalar subquery with limit 1:
// MariaDB turns an EXISTS into a semi-join that reads the person's or the
// tag's whole table before the first row). Never a derived table read
// whole, as SearchMedia's are, at every chunk.
//
// A photo's oldest copy (issue #201) is the row with no older copy of its
// content among the rows the search may show (`g`: outside the folders
// kept out of Images, under the folder of a folder's gallery, and media
// when no tag, person or collection ties the rows to it), older meaning an
// earlier `created` or, at the same second, a smaller path - SearchMedia's
// last row of the hash. An index lookup on `hash` per row looked at.
// `before` stays a per-row filter, which is equivalent (a photo's copies
// at or before it include its oldest); going up, the instant is compared
// with the oldest copy's date, never per row of the content (issue #194).

// MediaFilters is what an Images search keeps the photos by, besides the
// rules of its rows (kept out, media, before): SearchMediaRated's filters.
type MediaFilters struct {
	// Path is a folder's gallery: the rows directly in it.
	Path string
	// Tags keeps the photos with any of them (ranked by their score going
	// down, which BrowseMedia doesn't do: see search_lists.go).
	Tags []string
	// PersonIDs keeps the photos with a face of every one of them.
	PersonIDs []string
	// GroupID keeps a collection's photos.
	GroupID string
	// Area keeps the photos taken inside it (Places).
	Area *MediaArea
	// Rating filters (and ByRating ranks) a rated collection's photos;
	// only with GroupID.
	Rating *RatingQuery
}

// Filtered says whether m keeps anything out of the plain browse.
func (m *MediaFilters) Filtered() bool {
	return m.Path != "" || len(m.Tags) > 0 || len(m.PersonIDs) > 0 || m.GroupID != "" || m.Area != nil || m.rated() != nil
}

// media says whether the rows must be media to count (searchMediaClauses'
// rule): nothing else ties them to it without a tag, person or collection.
func (m *MediaFilters) media() bool {
	return len(m.Tags) == 0 && len(m.PersonIDs) == 0 && m.GroupID == ""
}

// rated is m's rating when it applies: a collection's, active.
func (m *MediaFilters) rated() *RatingQuery {
	if m.GroupID == "" || !m.Rating.Active() {
		return nil
	}
	return m.Rating
}

// peopleMatchable says whether any photo can have every person of ids:
// searchMediaClauses' count(distinct person_id) = len(ids) never holds
// for an id listed twice, nor for "" (a face taken off its person).
func peopleMatchable(ids []string) bool {
	for i, id := range ids {
		if id == "" || slices.Contains(ids[:i], id) {
			return false
		}
	}
	return true
}

// MediaBrowse is an Images search in date order, read by BrowseMedia (and
// the lists of search_lists.go): SearchMediaRated's (MediaBrowse.NewerThan:
// SearchMediaNewerThanWithin's) - never ranked by tags (going down) or by
// stars, which BrowseMedia would give in date order.
type MediaBrowse struct {
	MediaFilters
	// ImagesOnly leaves the videos out (SearchPhotos.include_videos off;
	// only where the rows must be media).
	ImagesOnly bool
	// Before keeps the photos dated at or before it (a jump, issue #77);
	// ignored going up.
	Before *time.Time
	// NewerThan, when set, browses up (issue #194): the photos whose oldest
	// copy is dated after it, oldest first.
	NewerThan *time.Time
	// Excluded is the folders kept out of Images (issue #192).
	Excluded []string
}

// BrowseMedia is the browse's next chunk: at least want photos after
// from (the next of the chunk before; nil for the first), in the browse's
// order, or every photo left, which done says. A chunk ends with every
// photo of its last second, so it may hold more than want, and when done
// is false it holds more than want: the photo after the want-th was
// there. next is where the chunk after it starts.
func (dao *Dao) BrowseMedia(b *MediaBrowse, from *time.Time, want int) (files []*pb.File, next *time.Time, done bool, err error) {
	up := b.NewerThan != nil
	if up {
		if from == nil {
			t := searchCutoff(*b.NewerThan)
			from = &t
		}
	}
	want = max(want, 0)
	joins, conds, args := b.rowClauses()
	gConds, gArgs := visibleRows("`g`", &b.MediaFilters, b.ImagesOnly, b.Excluded)
	oldest := "not exists (select 1 from `files` as `g` where `g`.`hash` = `f`.`hash`" + andAll(gConds) +
		" and (`g`.`created` < `f`.`created` or (`g`.`created` = `f`.`created` and `g`.`path` < `f`.`path`)))"
	dir, after := "desc", "`f`.`created` < ?"
	if up {
		dir, after = "asc", "`f`.`created` > ?"
	}
	selectFrom := func(cols string) string {
		return "select straight_join " + cols + " from `files` as `f`" + joins
	}

	// The dates of the photos from the chunk's start to the one after the
	// want-th, in index order: the rows passed over on the way (copies
	// whose photo is older, documents, what the filters leave out) are read
	// once, here.
	parts := slices.Clone(conds)
	queryArgs := slices.Clone(args)
	if from != nil {
		parts = append(parts, after)
		queryArgs = append(queryArgs, *from)
	}
	parts = append(parts, oldest)
	queryArgs = append(queryArgs, gArgs...)
	dates, err := dao.db.Query(selectFrom("`f`.`created`")+" where "+strings.Join(parts, " and ")+
		" order by `f`.`created` "+dir+fmt.Sprintf(" limit %d", want+1), queryArgs...)
	if err != nil {
		return nil, nil, false, err
	}
	var first, edge time.Time
	found := 0
	for dates.Next() {
		if err := dates.Scan(&edge); err != nil {
			dates.Close()
			return nil, nil, false, err
		}
		if found == 0 {
			first = edge
		}
		found++
	}
	if err := dates.Err(); err != nil {
		dates.Close()
		return nil, nil, false, err
	}
	dates.Close()
	if found == 0 {
		return nil, nil, true, nil
	}
	// Fewer than want+1: every photo left, done. Otherwise the chunk ends
	// with the whole second of the last one, where the next one starts.
	done = found <= want
	if !done {
		next = &edge
	}

	// The chunk: every photo from the first second found to the last, all
	// of it, sorted - nothing outside the span the dates covered.
	lo, hi := edge, first
	if up {
		lo, hi = first, edge
	}
	parts = append(slices.Clone(conds), "`f`.`created` >= ?", "`f`.`created` <= ?", oldest)
	queryArgs = append(append(slices.Clone(args), lo, hi), gArgs...)
	rows, err := dao.db.Query(selectFrom("`f`.`hash`, `f`.`mime`, `f`.`created`, `f`.`modified`, `f`.`path`, `f`.`size`")+
		" where "+strings.Join(parts, " and ")+" order by `f`.`created` "+dir+", `f`.`path` "+dir, queryArgs...)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		file := new(pb.File)
		var created, modified time.Time
		var size int64
		if err := rows.Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &size); err != nil {
			return nil, nil, false, err
		}
		file.Created = timestamppb.New(created)
		file.Modified = timestamppb.New(modified)
		SetFileSize(file, size)
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	return files, next, done, nil
}

// rowClauses is b's search as conditions on the rows of `f`, for a query
// that reads `files` in an order of its own (the `created` index, or a
// list's driver): joins on unique keys (the collection, its ratings, the
// area: one row or none per photo, and their args first), then the
// conditions, each one per row - the tags and people looked up by `hash`,
// the rows' own rules (searchMediaClauses': folder, kept out, media), the
// rating's filters, and before - with their args after the joins'.
// Going up there is no before and no rating, as SearchMediaNewerThanWithin
// has neither.
func (b *MediaBrowse) rowClauses() (joins string, conds []string, args []any) {
	m := &b.MediaFilters
	before, rating := b.Before, m.rated()
	if b.NewerThan != nil {
		before, rating = nil, nil
	}
	if m.GroupID != "" {
		joins += " join `image_group_files` as `ig` on `ig`.`group_id` = ? and `ig`.`hash` = `f`.`hash`"
		args = append(args, m.GroupID)
		if rating != nil {
			joins += " left join `photo_ratings` as `pr` on `pr`.`group_id` = `ig`.`group_id` and `pr`.`hash` = `f`.`hash`"
		}
	}
	if m.Area != nil {
		cond, condArgs := m.Area.condition("`pl`")
		joins += " join `photo_locations` as `pl` on `pl`.`hash` = `f`.`hash` and " + cond
		args = append(args, condArgs...)
	}
	if len(m.Tags) > 0 {
		ph, tagArgs := inPlaceholders(m.Tags)
		conds = append(conds, "(select 1 from `file_tags` as `xt` where `xt`.`hash` = `f`.`hash` and `xt`.`tag` in ("+ph+") limit 1) is not null")
		args = append(args, tagArgs...)
	}
	if len(m.PersonIDs) > 0 {
		if !peopleMatchable(m.PersonIDs) {
			conds = append(conds, "0 = 1")
		} else {
			for _, id := range m.PersonIDs {
				conds = append(conds, "(select 1 from `faces` as `xc` where `xc`.`hash` = `f`.`hash` and `xc`.`person_id` = ? limit 1) is not null")
				args = append(args, id)
			}
		}
	}
	visible, visibleArgs := visibleRows("`f`", m, b.ImagesOnly, b.Excluded)
	conds = append(conds, visible...)
	args = append(args, visibleArgs...)
	if rating != nil {
		conds = append(conds, rating.conditions()...)
	}
	if before != nil {
		conds = append(conds, "`f`.`created` <= ?")
		args = append(args, searchCutoff(*before))
	}
	return joins, conds, args
}

// visibleRows is the conditions that keep a row of alias the search may
// show: searchMediaClauses' own for its rows - directly in the folder of a
// folder's gallery, outside the folders kept out, and media where nothing
// else ties the rows to it.
func visibleRows(alias string, m *MediaFilters, imagesOnly bool, excluded []string) (conds []string, args []any) {
	if m.Path != "" {
		cond, condArgs := underPrefix(alias+".`path`", m.Path, true)
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}
	if cond, condArgs := outsideFolders(alias+".`path`", excluded); cond != "" {
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}
	if m.media() {
		if imagesOnly {
			conds = append(conds, alias+".`mime` like 'image%'")
		} else {
			conds = append(conds, "("+alias+".`mime` like 'image%' or "+alias+".`mime` like 'video%')")
		}
	}
	return conds, args
}

// andAll is conds, each after an " and ".
func andAll(conds []string) string {
	var out string
	for _, c := range conds {
		out += " and " + c
	}
	return out
}
