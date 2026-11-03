// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"fmt"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #206: Images' plain browse - the whole library, with no tag,
// person, collection, area, text or rating - is read a chunk at a time by
// keyset instead of whole. SearchMedia reads every row of a search,
// sorted in a filesort, and keeps each photo's oldest copy in Go: at 200k
// photos ~100 MB and seconds on a Pi before the first tiles, at every new
// search. BrowseMedia gives the same photos in the same order (each at its
// oldest copy among those Images may show, newest first and, at one
// instant, by path backwards; oldest first going up), a chunk at a time.
//
// The key is (`created`, `path`): `path` is unique, so it orders the rows
// completely. No index holds both (`path` alone is InnoDB's whole key
// limit, release 143's script; and with no PRIMARY KEY, MariaDB doesn't
// extend the `created` index by it), but `files` has had one on `created`
// from the start, and `created` is a DATETIME - whole seconds. So a chunk
// is whole seconds, in two statements: the dates of its photos and the one
// after them, read in the index's order and stopping there (`order by
// created ... limit want+1`: no filesort, and the rows passed over - copies
// whose photo is older, documents - are read once, there); then every
// photo from the first of those seconds to the last, sorted in SQL by
// (`created`, `path`) - a filesort of a chunk, never of the library. The
// next chunk starts after the last second. A second holding very many
// rows (a sync that stamped them all alike) makes its chunk as large; at
// worst that is the whole library, as before.
//
// A photo's oldest copy (issue #201) is the row with no older copy of its
// content among the rows Images may show (`g`: outside the folders kept
// out of Images, and media), older meaning an earlier `created` or, at the
// same second, a smaller path - SearchMedia's last row of the hash. An
// index lookup on `hash` per row looked at. `before` stays a per-row
// filter, which is equivalent (a photo's copies at or before it include
// its oldest); going up, the instant is compared with the oldest copy's
// date, never per row of the content (issue #194).

// MediaBrowse is a plain browse of Images: SearchMedia with no path, tag,
// person, collection or area (MediaBrowse.NewerThan: SearchMediaNewerThan).
type MediaBrowse struct {
	// ImagesOnly leaves the videos out (SearchPhotos.include_videos off).
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
	before := b.Before
	if up {
		before = nil
		if from == nil {
			t := searchCutoff(*b.NewerThan)
			from = &t
		}
	}
	want = max(want, 0)
	// The rows SearchMedia reads for the same browse, before and all.
	_, where, _, _, args := searchMediaClauses("", nil, nil, "", b.ImagesOnly, before, b.Excluded, nil)
	gWhere, gArgs := browseVisible("`g`", b.ImagesOnly, b.Excluded)
	oldest := " and not exists (select 1 from `files` as `g` where `g`.`hash` = `f`.`hash`" + gWhere +
		" and (`g`.`created` < `f`.`created` or (`g`.`created` = `f`.`created` and `g`.`path` < `f`.`path`)))"
	dir, after := "desc", " and `f`.`created` < ?"
	if up {
		dir, after = "asc", " and `f`.`created` > ?"
	}

	// The dates of the photos from the chunk's start to the one after the
	// want-th, in index order: the rows passed over on the way (copies
	// whose photo is older, documents) are read once, here.
	queryArgs := append([]any{}, args...)
	bound := ""
	if from != nil {
		bound = after
		queryArgs = append(queryArgs, *from)
	}
	queryArgs = append(queryArgs, gArgs...)
	dates, err := dao.db.Query("select `f`.`created` from `files` as `f`"+where+bound+oldest+
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
	queryArgs = append(append([]any{}, args...), lo, hi)
	queryArgs = append(queryArgs, gArgs...)
	bound = " and `f`.`created` >= ? and `f`.`created` <= ?"
	rows, err := dao.db.Query("select `f`.`hash`, `f`.`mime`, `f`.`created`, `f`.`modified`, `f`.`path`, `f`.`size` from `files` as `f`"+
		where+bound+oldest+" order by `f`.`created` "+dir+", `f`.`path` "+dir, queryArgs...)
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

// browseVisible is the conditions, each after an " and ", that keep a row
// of alias Images may show in a plain browse: searchMediaClauses' own for
// `f` without a tag, person or collection (outside excluded, and media).
func browseVisible(alias string, imagesOnly bool, excluded []string) (string, []any) {
	var where string
	cond, args := outsideFolders(alias+".`path`", excluded)
	if cond != "" {
		where += " and " + cond
	}
	if imagesOnly {
		where += " and " + alias + ".`mime` like 'image%'"
	} else {
		where += " and (" + alias + ".`mime` like 'image%' or " + alias + ".`mime` like 'video%')"
	}
	return where, args
}
