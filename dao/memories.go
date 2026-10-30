// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"time"
)

// On this day (Memories): what choosing a day's photos reads - the photos
// taken on the days asked for, each with its measures (photo_quality.go),
// tags, people, collections and posts - and what the user chose never to
// see again (memories_hidden). The choosing itself is
// files_manager/memories.go's.

// MemoryCandidate is one photo of Images taken on one of the days asked
// for: its oldest copy among those Images may show (the copy, and date,
// Images shows it at - issue #201), and its measures (nil: not measured
// yet).
type MemoryCandidate struct {
	Hash, Path, Mime string
	Created          time.Time
	Quality          *PhotoQuality
}

// MemoryCandidates is every photo (images only, as Images browses) whose
// oldest copy outside excluded was taken on one of monthDays ("10-30",
// month-day in UTC, as files.created is kept). The caller asks for the days
// around the ones it wants and keeps those in the user's own time zone.
// Two queries: the copies on those days, then each one's oldest copy, so a
// photo synced back years later (a copy dated that day) never counts on
// the day it came back.
func (dao *Dao) MemoryCandidates(monthDays []string, excluded []string) ([]MemoryCandidate, error) {
	if len(monthDays) == 0 {
		return nil, nil
	}
	ph, args := inPlaceholders(monthDays)
	where := " where `f`.`mime` like 'image%' and date_format(`f`.`created`, '%m-%d') in (" + ph + ")"
	if cond, condArgs := outsideFolders("`f`.`path`", excluded); cond != "" {
		where += " and " + cond
		args = append(args, condArgs...)
	}
	rows, err := dao.db.Query("select `f`.`hash`, `f`.`path`, `f`.`mime`, `f`.`created`,"+
		" `q`.`hash`, `q`.`aesthetic`, `q`.`sharpness`, `q`.`dark`, `q`.`bright`, `q`.`brightness`, `q`.`width`, `q`.`height`, `q`.`camera`"+
		" from `files` as `f` left join `photo_quality` as `q` on `q`.`hash` = `f`.`hash`"+where, args...)
	if isNoSuchTable(err) {
		// Before the release's script: no measures yet, the photos still are.
		rows, err = dao.db.Query("select `f`.`hash`, `f`.`path`, `f`.`mime`, `f`.`created`,"+
			" null, null, null, null, null, null, null, null, null from `files` as `f`"+where, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byHash := map[string]*MemoryCandidate{}
	var order []string
	for rows.Next() {
		var c MemoryCandidate
		var qHash sql.NullString
		var aesthetic, sharpness, dark, bright, brightness sql.NullFloat64
		var width, height sql.NullInt64
		var camera sql.NullBool
		if err := rows.Scan(&c.Hash, &c.Path, &c.Mime, &c.Created, &qHash, &aesthetic, &sharpness, &dark, &bright, &brightness, &width, &height, &camera); err != nil {
			return nil, err
		}
		if qHash.Valid {
			q := &PhotoQuality{Sharpness: sharpness.Float64, Dark: dark.Float64, Bright: bright.Float64, Brightness: brightness.Float64,
				Width: int(width.Int64), Height: int(height.Int64)}
			if aesthetic.Valid {
				a := float32(aesthetic.Float64)
				q.Aesthetic = &a
			}
			if camera.Valid {
				q.Camera = &camera.Bool
			}
			c.Quality = q
		}
		old, ok := byHash[c.Hash]
		if !ok {
			byHash[c.Hash] = &c
			order = append(order, c.Hash)
		} else if c.Created.Before(old.Created) || (c.Created.Equal(old.Created) && c.Path < old.Path) {
			c.Quality = old.Quality
			*old = c
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	oldest, err := dao.oldestCopyDates(order, excluded)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryCandidate, 0, len(order))
	for _, h := range order {
		c := byHash[h]
		if t, ok := oldest[h]; ok && t.Before(c.Created) {
			continue // an older copy, on another day, is the one Images shows
		}
		out = append(out, *c)
	}
	return out, nil
}

// oldestCopyDates is each hash's earliest files.created among its copies
// outside excluded.
func (dao *Dao) oldestCopyDates(hashes []string, excluded []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(hashes))
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		where := " where `hash` in (" + ph + ")"
		if cond, condArgs := outsideFolders("`path`", excluded); cond != "" {
			where += " and " + cond
			args = append(args, condArgs...)
		}
		rows, err := dao.db.Query("select `hash`, min(`created`) from `files`"+where+" group by `hash`", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			var t time.Time
			if err := rows.Scan(&h, &t); err != nil {
				rows.Close()
				return nil, err
			}
			out[h] = t
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MemoryTag is one of a photo's tags.
type MemoryTag struct {
	Name  string
	Score float32
}

// TagsOfHashes is every tag of each of hashes.
func (dao *Dao) TagsOfHashes(hashes []string) (map[string][]MemoryTag, error) {
	out := map[string][]MemoryTag{}
	err := eachChunkRow(dao, hashes, "select `hash`, `tag`, `score` from `file_tags` where `hash` in (%s)", func(rows *sql.Rows) error {
		var h string
		var t MemoryTag
		if err := rows.Scan(&h, &t.Name, &t.Score); err != nil {
			return err
		}
		out[h] = append(out[h], t)
		return nil
	})
	return out, err
}

// MemoryFace is a person face recognition found in a photo; Name is ""
// for someone not named yet.
type MemoryFace struct {
	PersonID, Name string
}

// FacesOfHashes is the people found in each of hashes, once each (a face
// taken off its person, person_id "", is no one's).
func (dao *Dao) FacesOfHashes(hashes []string) (map[string][]MemoryFace, error) {
	out := map[string][]MemoryFace{}
	err := eachChunkRow(dao, hashes, "select distinct `f`.`hash`, `f`.`person_id`, coalesce(`p`.`name`, '') from `faces` as `f`"+
		" left join `people` as `p` on `p`.`id` = `f`.`person_id` where `f`.`person_id` <> '' and `f`.`hash` in (%s)", func(rows *sql.Rows) error {
		var h string
		var f MemoryFace
		if err := rows.Scan(&h, &f.PersonID, &f.Name); err != nil {
			return err
		}
		out[h] = append(out[h], f)
		return nil
	})
	return out, err
}

// CollectedHashes is which of hashes are in a collection.
func (dao *Dao) CollectedHashes(hashes []string) (map[string]bool, error) {
	out := map[string]bool{}
	err := eachChunkRow(dao, hashes, "select distinct `hash` from `image_group_files` where `hash` in (%s)", func(rows *sql.Rows) error {
		var h string
		if err := rows.Scan(&h); err != nil {
			return err
		}
		out[h] = true
		return nil
	})
	return out, err
}

// PostStats is what the user's own posts of a photo got.
type PostStats struct {
	Posts, Likes, Comments int
}

// PostedHashes is, for each of hashes the user posted, how many of their
// own posts hold it and the likes and comments those got.
func (dao *Dao) PostedHashes(hashes []string) (map[string]PostStats, error) {
	out := map[string]PostStats{}
	err := eachChunkRow(dao, hashes, "select `pf`.`hash`, count(distinct `p`.`uuid`),"+
		" coalesce(sum((select count(*) from `social_publication_likes` as `l` where `l`.`pub_uuid` = `p`.`uuid`)), 0),"+
		" coalesce(sum((select count(*) from `social_publications_comments` as `c` where `c`.`pub_uuid` = `p`.`uuid`)), 0)"+
		" from `social_publications_files` as `pf` join `social_publications` as `p` on `p`.`uuid` = `pf`.`uuid`"+
		" where `p`.`own_publication` = 1 and `pf`.`hash` in (%s) group by `pf`.`hash`", func(rows *sql.Rows) error {
		var h string
		var s PostStats
		if err := rows.Scan(&h, &s.Posts, &s.Likes, &s.Comments); err != nil {
			return err
		}
		out[h] = s
		return nil
	})
	return out, err
}

// TagCount is how many photos carry a tag.
type TagCount struct {
	Tag   string
	Count int
}

// PlaceTagCounts is the tags written from a photo's own location (issue
// #42: a city and a country, score 1) by how many photos carry them, the
// commonest first, at most limit: where the library's photos are usually
// taken. Tags the tagging model gave a full score are among them; the
// caller keeps the names geotag knows.
func (dao *Dao) PlaceTagCounts(limit int) ([]TagCount, error) {
	rows, err := dao.db.Query("select `tag`, count(*) as `n` from `file_tags` where `score` >= 1 group by `tag` order by `n` desc, `tag` asc limit ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// eachChunkRow runs query (one %s for the placeholders) for hashes, in
// chunks, calling row for each row.
func eachChunkRow(dao *Dao, hashes []string, query string, row func(*sql.Rows) error) error {
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		rows, err := dao.db.Query(fmt.Sprintf(query, ph), args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err := row(rows); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// HiddenMemory is something the user chose not to see in On this day: a
// photo (by content hash), a day ("2023-10-30": every photo taken that day,
// in any memory), a memory ("week:2023-10-27", its key) or a person (by id).
type HiddenMemory struct {
	Kind, Value string
}

// The kinds of HiddenMemory.
const (
	HideMemoryPhoto  = "photo"
	HideMemoryDay    = "day"
	HideMemoryMemory = "memory"
	HideMemoryPerson = "person"
)

// HiddenMemories is everything hidden. Before the release's script nothing
// is (and nothing can be hidden: ErrSchemaBehind on HideMemory).
func (dao *Dao) HiddenMemories() ([]HiddenMemory, error) {
	rows, err := dao.db.Query("select `kind`, `value` from `memories_hidden`")
	if isNoSuchTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HiddenMemory
	for rows.Next() {
		var h HiddenMemory
		if err := rows.Scan(&h.Kind, &h.Value); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HideMemory hides h; hidden again is no change.
func (dao *Dao) HideMemory(h HiddenMemory) error {
	_, err := dao.db.Exec("insert into `memories_hidden` (`kind`, `value`, `hidden`) values (?, ?, ?)"+
		" on duplicate key update `hidden` = `hidden`", h.Kind, h.Value, time.Now().UTC())
	return schemaBehindTable(err)
}

// ShowMemory shows h again.
func (dao *Dao) ShowMemory(h HiddenMemory) error {
	_, err := dao.db.Exec("delete from `memories_hidden` where `kind` = ? and `value` = ?", h.Kind, h.Value)
	return schemaBehindTable(err)
}

// ShowAllMemories shows everything hidden again.
func (dao *Dao) ShowAllMemories() error {
	_, err := dao.db.Exec("delete from `memories_hidden`")
	return schemaBehindTable(err)
}
