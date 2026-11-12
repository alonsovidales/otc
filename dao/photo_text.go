// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Text in photos (release 180): the text a text model read in each photo
// (files_manager/photo_text.go), by content, kept sealed with the user's
// key - the text of a document, an ID or an address is as private as the
// photo - only while the user has it on (settings.text_in_photos_enabled):
// turning it off deletes every row. A photo read that has no text has a row
// with no text, so it isn't read again. See db.sql's photo_text.

// SetPhotoText records hash's text read by model, sealed - nil for a photo
// with none - replacing any it had. ErrSchemaBehind without the table (the
// release's script hasn't run).
func (dao *Dao) SetPhotoText(hash, model string, sealed []byte) error {
	_, err := dao.db.Exec("insert into `photo_text` (`hash`, `model`, `text`, `made`) values (?, ?, ?, ?)"+
		" on duplicate key update `model` = values(`model`), `text` = values(`text`), `made` = values(`made`)",
		hash, model, sealed, time.Now().UTC())
	return schemaBehindTable(err)
}

// PhotoText is hash's sealed text read by model: read false when it hasn't
// been (or another model read it), sealed nil when it has none.
// ErrSchemaBehind without the table.
func (dao *Dao) PhotoText(hash, model string) (sealed []byte, read bool, err error) {
	err = dao.db.QueryRow("select `text` from `photo_text` where `hash` = ? and `model` = ?", hash, model).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, schemaBehindTable(err)
	}
	return sealed, true, nil
}

// DelPhotoTexts forgets the text of content nothing uses any more, or kept
// out of Images. Without the table there is nothing to forget.
func (dao *Dao) DelPhotoTexts(hashes ...string) error {
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		_, err := dao.db.Exec("delete from `photo_text` where `hash` in ("+ph+")", args...)
		if isNoSuchTable(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// EachPhotoText calls fn with every text read by model, sealed, in no
// particular order - the photos read that have none are left out; fn's
// error stops it and is returned. ErrSchemaBehind without the table.
func (dao *Dao) EachPhotoText(model string, fn func(hash string, sealed []byte) error) error {
	rows, err := dao.db.Query("select `hash`, `text` from `photo_text` where `model` = ? and `text` is not null", model)
	if err != nil {
		return schemaBehindTable(err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var sealed []byte
		if err := rows.Scan(&hash, &sealed); err != nil {
			return err
		}
		if err := fn(hash, sealed); err != nil {
			return err
		}
	}
	return rows.Err()
}

// TextInPhotosEnabled is whether Text in photos is on for this library
// (settings.text_in_photos_enabled, on unless the user turns it off).
// ErrSchemaBehind before the release that added it, which callers read as
// off.
func (dao *Dao) TextInPhotosEnabled() (on bool, err error) {
	err = dao.db.QueryRow("select `text_in_photos_enabled` from `settings`").Scan(&on)
	return on, schemaBehind(err)
}

// SetTextInPhotosEnabled turns Text in photos on or off. Off deletes every
// text in the same transaction: the switch never says off while one is
// kept.
func (dao *Dao) SetTextInPhotosEnabled(on bool) error {
	if on {
		_, err := dao.db.Exec("update `settings` set `text_in_photos_enabled` = 1")
		return schemaBehind(err)
	}
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("update `settings` set `text_in_photos_enabled` = 0"); err != nil {
		tx.Rollback()
		return schemaBehind(err)
	}
	if _, err := tx.Exec("delete from `photo_text`"); err != nil && !isNoSuchTable(err) {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// VisionWork is a photo the background pass of Text in photos and Pets
// still has work on (PhotosToLookAt): its file (its smallest path and
// mime), whether its text was read and whether it was looked at for pets,
// each by the model named, and its Smart search vector, sealed - nil
// without one by the model named - which says whether it may show a pet.
type VisionWork struct {
	File    *pb.File
	HasText bool
	Scanned bool
	Vec     []byte
}

// PhotosToLookAt is the next limit photos, by content after afterHash in
// order, whose text isn't read by textModel yet (while text) or that
// weren't looked at for pets by petModel (while pets). Only photos Images
// may show: a copy outside excluded (issue #192). ErrSchemaBehind without
// the tables.
func (dao *Dao) PhotosToLookAt(afterHash string, limit int, text bool, textModel string, pets bool, petModel, vecModel string, excluded []string) ([]VisionWork, error) {
	where := " where `f`.`mime` like 'image%' and `f`.`hash` > ?"
	args := []any{textModel, petModel, vecModel, afterHash}
	if cond, condArgs := outsideFolders("`f`.`path`", excluded); cond != "" {
		where += " and " + cond
		args = append(args, condArgs...)
	}
	args = append(args, text, pets, limit)
	rows, err := dao.db.Query(
		"select `f`.`hash`, min(`f`.`mime`), min(`f`.`created`), min(`f`.`modified`), min(`f`.`path`), min(`f`.`size`),"+
			" coalesce(max(`t`.`model` = ?), 0) as `has_text`, coalesce(max(`s`.`model` = ?), 0) as `scanned`,"+
			" max(case when `e`.`model` = ? then `e`.`vec` end) from `files` as `f`"+
			" left join `photo_text` as `t` on `t`.`hash` = `f`.`hash`"+
			" left join `pet_scans` as `s` on `s`.`hash` = `f`.`hash`"+
			" left join `photo_embeddings` as `e` on `e`.`hash` = `f`.`hash`"+where+
			" group by `f`.`hash` having (? and `has_text` = 0) or (? and `scanned` = 0)"+
			" order by `f`.`hash` asc limit ?", args...)
	if err != nil {
		return nil, schemaBehindTable(err)
	}
	defer rows.Close()
	var out []VisionWork
	for rows.Next() {
		w := VisionWork{File: new(pb.File)}
		var created, modified time.Time
		var size int64
		if err := rows.Scan(&w.File.Hash, &w.File.Mime, &created, &modified, &w.File.Path, &size, &w.HasText, &w.Scanned, &w.Vec); err != nil {
			return nil, err
		}
		w.File.Created = timestamppb.New(created)
		w.File.Modified = timestamppb.New(modified)
		SetFileSize(w.File, size)
		out = append(out, w)
	}
	return out, rows.Err()
}

// CountPhotosToRead is how many photos Images may show have no text read
// by model yet: what the background pass has left to read.
func (dao *Dao) CountPhotosToRead(model string, excluded []string) (int64, error) {
	return dao.countPhotosWithout("photo_text", model, excluded)
}

// CountPhotosToScanForPets is how many photos Images may show weren't
// looked at for pets by model yet.
func (dao *Dao) CountPhotosToScanForPets(model string, excluded []string) (int64, error) {
	return dao.countPhotosWithout("pet_scans", model, excluded)
}

// countPhotosWithout counts the photos with no row of model in table (a
// constant of this file's).
func (dao *Dao) countPhotosWithout(table, model string, excluded []string) (n int64, err error) {
	where := " where (`x`.`hash` is null or `x`.`model` <> ?) and `f`.`mime` like 'image%'"
	args := []any{model}
	if cond, condArgs := outsideFolders("`f`.`path`", excluded); cond != "" {
		where += " and " + cond
		args = append(args, condArgs...)
	}
	err = dao.db.QueryRow("select count(distinct `f`.`hash`) from `files` as `f`"+
		" left join `"+table+"` as `x` on `x`.`hash` = `f`.`hash`"+where, args...).Scan(&n)
	return n, schemaBehindTable(err)
}

// PhotoEmbedding is hash's Smart search vector made by model, sealed; nil
// without one. ErrSchemaBehind without the table.
func (dao *Dao) PhotoEmbedding(hash, model string) ([]byte, error) {
	var sealed []byte
	err := dao.db.QueryRow("select `vec` from `photo_embeddings` where `hash` = ? and `model` = ?", hash, model).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return sealed, schemaBehindTable(err)
}
