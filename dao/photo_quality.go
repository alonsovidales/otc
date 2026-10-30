// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// On this day (Memories): how each photo looks, measured once by content
// from its big thumbnail (files_manager/quality.go), so that choosing a
// day's best photos is a query, not a pass over the pictures. Kept as
// numbers, unencrypted, as file_tags' scores are: they say how sharp and
// well exposed a photo is, not what it shows. See db.sql's photo_quality.

// PhotoQuality is one photo's measures.
type PhotoQuality struct {
	// Aesthetic is NIMA's score, 1 to 10; nil when the photo was measured
	// without the model (its file missing): measured again once it is
	// there.
	Aesthetic *float32
	// Sharpness is the variance of the thumbnail's Laplacian.
	Sharpness float64
	// Dark and Bright are the shares of pixels clipped to black and to
	// white; Brightness the mean luma, 0 to 255.
	Dark, Bright, Brightness float64
	// Width and Height are the big thumbnail's: never larger than the
	// photo, so a small one says the photo is small.
	Width, Height int
	// Camera: the photo's metadata names a camera make or model (a phone's
	// screenshot and a downloaded picture don't); nil when the metadata
	// couldn't be read.
	Camera *bool
}

// SetPhotoQuality records hash's measures, replacing any it had.
// ErrSchemaBehind without the table (the release's script hasn't run).
func (dao *Dao) SetPhotoQuality(hash string, q PhotoQuality) error {
	var aesthetic sql.NullFloat64
	if q.Aesthetic != nil {
		aesthetic = sql.NullFloat64{Float64: float64(*q.Aesthetic), Valid: true}
	}
	var camera sql.NullBool
	if q.Camera != nil {
		camera = sql.NullBool{Bool: *q.Camera, Valid: true}
	}
	_, err := dao.db.Exec("insert into `photo_quality` (`hash`, `aesthetic`, `sharpness`, `dark`, `bright`, `brightness`, `width`, `height`, `camera`, `scored`)"+
		" values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"+
		" on duplicate key update `aesthetic` = values(`aesthetic`), `sharpness` = values(`sharpness`), `dark` = values(`dark`),"+
		" `bright` = values(`bright`), `brightness` = values(`brightness`), `width` = values(`width`), `height` = values(`height`),"+
		" `camera` = values(`camera`), `scored` = values(`scored`)",
		hash, aesthetic, q.Sharpness, q.Dark, q.Bright, q.Brightness, q.Width, q.Height, camera, time.Now().UTC())
	return schemaBehindTable(err)
}

// DelPhotoQuality forgets the measures of content nothing uses any more.
// Without the table there is nothing to forget.
func (dao *Dao) DelPhotoQuality(hash string) error {
	_, err := dao.db.Exec("delete from `photo_quality` where `hash` = ?", hash)
	if isNoSuchTable(err) {
		return nil
	}
	return err
}

// PhotosToScore is the next limit photos, by content after afterHash in
// order, that have no measures yet - or, with withoutAesthetic, were
// measured without the quality model: what the backfill still has to
// score. Only photos Images may show: a copy outside excluded (issue #192:
// content only kept-out folders hold is never analysed). One row per
// content, its smallest path and mime. ErrSchemaBehind without the table.
func (dao *Dao) PhotosToScore(afterHash string, limit int, withoutAesthetic bool, excluded []string) (files []*pb.File, err error) {
	missing := "`q`.`hash` is null"
	if withoutAesthetic {
		missing = "(`q`.`hash` is null or `q`.`aesthetic` is null)"
	}
	where := " where " + missing + " and `f`.`mime` like 'image%' and `f`.`hash` > ?"
	args := []any{afterHash}
	if cond, condArgs := outsideFolders("`f`.`path`", excluded); cond != "" {
		where += " and " + cond
		args = append(args, condArgs...)
	}
	args = append(args, limit)
	rows, err := dao.db.Query(
		"select `f`.`hash`, min(`f`.`mime`), min(`f`.`created`), min(`f`.`modified`), min(`f`.`path`), min(`f`.`size`) from `files` as `f`"+
			" left join `photo_quality` as `q` on `q`.`hash` = `f`.`hash`"+where+
			" group by `f`.`hash` order by `f`.`hash` asc limit ?", args...)
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

// MemoriesEnabled is whether On this day is on for this library
// (settings.memories_enabled, on unless the user turns it off).
// ErrSchemaBehind before the release that added it, which callers read as
// off: the device has nowhere to keep the measures yet.
func (dao *Dao) MemoriesEnabled() (on bool, err error) {
	err = dao.db.QueryRow("select `memories_enabled` from `settings`").Scan(&on)
	return on, schemaBehind(err)
}

// SetMemoriesEnabled turns On this day on or off. Nothing is deleted: the
// measures say nothing about what a photo shows, and turning it on again
// needs no new pass.
func (dao *Dao) SetMemoriesEnabled(on bool) error {
	_, err := dao.db.Exec("update `settings` set `memories_enabled` = ?", on)
	return schemaBehind(err)
}
