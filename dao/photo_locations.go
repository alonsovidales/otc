// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Places, the map in Images: where each photo or video was taken, kept by
// content hash in photo_locations (see db.sql). Written when the device
// processes the content (files_manager.processMedia) and once in the
// background for what it held before (files_manager.backfillLocations);
// read by the map's index (EachLocatedCopy) and by a search inside an area
// (searchMediaClauses). Only while Places is on (settings.places_enabled,
// files_manager's placesSwitch): turning it off empties the table. A
// database without the table (the release's script hasn't reached it)
// reads as one where nothing has a position, and its writes are
// ErrSchemaBehind.

// MediaArea is an area of the map in degrees, bounds inclusive
// (PlaceArea in the protocol). West greater than East is an area across
// the antimeridian, from West eastwards to East.
type MediaArea struct {
	South, West, North, East float64
}

// condition is the SQL keeping the rows of table t (a photo_locations
// alias) whose position is in a, with its args in order. A row without a
// position (NULL) is in no area.
func (a *MediaArea) condition(t string) (string, []any) {
	lat := t + ".`lat` between ? and ?"
	if a.West > a.East {
		return lat + " and (" + t + ".`lon` >= ? or " + t + ".`lon` <= ?)", []any{a.South, a.North, a.West, a.East}
	}
	return lat + " and " + t + ".`lon` between ? and ?", []any{a.South, a.North, a.West, a.East}
}

// schemaBehindTable wraps a missing-table error in ErrSchemaBehind.
func schemaBehindTable(err error) error {
	if isNoSuchTable(err) {
		return fmt.Errorf("%w: %v", ErrSchemaBehind, err)
	}
	return err
}

// SetPhotoLocation records where the content hash was taken, or - ok
// false - that it was looked at and has no position, so nothing reads its
// metadata for this again (the backfill lists only content without a row).
func (dao *Dao) SetPhotoLocation(hash string, lat, lon float64, ok bool) error {
	la := sql.NullFloat64{Float64: lat, Valid: ok}
	lo := sql.NullFloat64{Float64: lon, Valid: ok}
	_, err := dao.db.Exec("insert into `photo_locations` (`hash`, `lat`, `lon`) values (?, ?, ?)"+
		" on duplicate key update `lat` = values(`lat`), `lon` = values(`lon`)", hash, la, lo)
	return schemaBehindTable(err)
}

// DelPhotoLocation forgets the position of content nothing uses any more.
// Without the table there is nothing to forget.
func (dao *Dao) DelPhotoLocation(hash string) error {
	_, err := dao.db.Exec("delete from `photo_locations` where `hash` = ?", hash)
	if isNoSuchTable(err) {
		return nil
	}
	return err
}

// LocatedCopy is one copy (a files row) of a photo or video with a
// position, as the map's index reads them: its content, where it was
// taken, the copy's date and whether its mime is a video's.
type LocatedCopy struct {
	Hash     string
	Lat, Lon float64
	Created  time.Time
	Video    bool
}

// EachLocatedCopy calls fn for every copy Images may show (outside
// excluded, the folders kept out of Images) of a photo or video with a
// position, in no particular order: a content at three paths comes three
// times, and the caller takes its oldest copy's date (the photo search's,
// issue #201). Not grouped here: MariaDB took five times as long to group
// 100,000 rows by hash as to send them. Without the table there are none.
// fn's error stops the read and is returned.
func (dao *Dao) EachLocatedCopy(excluded []string, fn func(LocatedCopy) error) error {
	where := "`l`.`lat` is not null and `l`.`lon` is not null and (`f`.`mime` like 'image%' or `f`.`mime` like 'video%')"
	cond, args := outsideFolders("`f`.`path`", excluded)
	if cond != "" {
		where += " and " + cond
	}
	rows, err := dao.db.Query("select `f`.`hash`, `f`.`created`, `f`.`mime` like 'video%', `l`.`lat`, `l`.`lon`"+
		" from `files` as `f` join `photo_locations` as `l` on `l`.`hash` = `f`.`hash` where "+where, args...)
	if isNoSuchTable(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m LocatedCopy
		var video int
		if err := rows.Scan(&m.Hash, &m.Created, &video, &m.Lat, &m.Lon); err != nil {
			return err
		}
		m.Video = video == 1
		if err := fn(m); err != nil {
			return err
		}
	}
	return rows.Err()
}

// EachMatchingMediaHash calls fn with the content of every photo the
// search with these filters would show (SearchMedia's photos, one per
// content), without reading their rows: the map's places for a filtered
// search are the index's photos among these.
func (dao *Dao) EachMatchingMediaHash(tags []string, personIDs []string, groupID string, imagesOnly bool, excluded []string, fn func(hash string)) error {
	from, where, _, _, args := searchMediaClauses("", tags, personIDs, groupID, imagesOnly, nil, excluded, nil)
	rows, err := dao.db.Query("select distinct `f`.`hash` "+from+where, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return err
		}
		fn(h)
	}
	return rows.Err()
}

// ShownCopies is, for each of hashes, the row the photo search shows it
// at: its oldest copy outside excluded - the earliest, then the smallest
// path (SearchMedia's oldestCopies). A hash with no such row is left out.
func (dao *Dao) ShownCopies(hashes []string, excluded []string) (map[string]*pb.File, error) {
	out := make(map[string]*pb.File, len(hashes))
	for start := 0; start < len(hashes); start += 500 {
		batch := hashes[start:min(start+500, len(hashes))]
		args := make([]any, 0, len(batch))
		for _, h := range batch {
			args = append(args, h)
		}
		query := "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` in (" +
			strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + ")"
		if cond, condArgs := outsideFolders("`path`", excluded); cond != "" {
			query += " and " + cond
			args = append(args, condArgs...)
		}
		if err := dao.shownCopiesQuery(query, args, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (dao *Dao) shownCopiesQuery(query string, args []any, out map[string]*pb.File) error {
	rows, err := dao.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		file := new(pb.File)
		var created, modified time.Time
		var size int64
		if err := rows.Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &size); err != nil {
			return err
		}
		file.Created = timestamppb.New(created)
		file.Modified = timestamppb.New(modified)
		SetFileSize(file, size)
		if old, ok := out[file.Hash]; !ok || shownBefore(file, old) {
			out[file.Hash] = file
		}
	}
	return rows.Err()
}

// shownBefore is whether a is the copy the photo search shows rather than
// b, of the same content: the earlier, then the smaller path.
func shownBefore(a, b *pb.File) bool {
	ac, bc := a.GetCreated().AsTime(), b.GetCreated().AsTime()
	if !ac.Equal(bc) {
		return ac.Before(bc)
	}
	return a.Path < b.Path
}

// MediaWithoutLocation is the next limit photos and videos, by content
// after afterHash in order, that have no photo_locations row yet: what the
// backfill still has to read. One row per content (its smallest path and
// mime, as ListMediaForReprocess). ErrSchemaBehind without the table.
func (dao *Dao) MediaWithoutLocation(afterHash string, limit int) (files []*pb.File, err error) {
	rows, err := dao.db.Query(
		"select `f`.`hash`, min(`f`.`mime`), min(`f`.`created`), min(`f`.`modified`), min(`f`.`path`), min(`f`.`size`) from `files` as `f`"+
			" left join `photo_locations` as `l` on `l`.`hash` = `f`.`hash`"+
			" where `l`.`hash` is null and (`f`.`mime` like 'image%' or `f`.`mime` like 'video%') and `f`.`hash` > ?"+
			" group by `f`.`hash` order by `f`.`hash` asc limit ?",
		afterHash, limit)
	if err != nil {
		return nil, schemaBehindTable(err)
	}
	defer rows.Close()
	for rows.Next() {
		file := new(pb.File)
		var created, modified time.Time
		var size int64
		if err := rows.Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &size); err != nil {
			return nil, err
		}
		file.Created = timestamppb.New(created)
		file.Modified = timestamppb.New(modified)
		SetFileSize(file, size)
		files = append(files, file)
	}
	return files, rows.Err()
}

// LocationsBackfilled is whether the backfill of positions has run to the
// end on this database (settings.locations_backfilled). ErrSchemaBehind
// before the release that added it.
func (dao *Dao) LocationsBackfilled() (done bool, err error) {
	err = dao.db.QueryRow("select `locations_backfilled` from `settings`").Scan(&done)
	return done, schemaBehind(err)
}

// SetLocationsBackfilled records that it has.
func (dao *Dao) SetLocationsBackfilled() error {
	_, err := dao.db.Exec("update `settings` set `locations_backfilled` = 1")
	return schemaBehind(err)
}

// PlacesEnabled is whether Places is on (settings.places_enabled, off
// unless the user turns it on): only then does the device keep where its
// photos were taken. ErrSchemaBehind before the release that added it,
// which callers read as off.
func (dao *Dao) PlacesEnabled() (on bool, err error) {
	err = dao.db.QueryRow("select `places_enabled` from `settings`").Scan(&on)
	return on, schemaBehind(err)
}

// SetPlacesEnabled turns Places on, or off. Off deletes every position
// kept and the record that the backfill ran, with the switch in one
// transaction: nothing of them is left, and turning it on again reads
// them afresh.
func (dao *Dao) SetPlacesEnabled(on bool) error {
	if on {
		_, err := dao.db.Exec("update `settings` set `places_enabled` = 1")
		return schemaBehind(err)
	}
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("update `settings` set `places_enabled` = 0, `locations_backfilled` = 0"); err != nil {
		tx.Rollback()
		return schemaBehind(err)
	}
	if _, err := tx.Exec("delete from `photo_locations`"); err != nil && !isNoSuchTable(err) {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
