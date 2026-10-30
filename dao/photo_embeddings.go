// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Smart search (beta): each photo described as a vector by an image model
// (files_manager/smart_search.go), by content, kept sealed with the user's
// key - unlike file_tags' words, a vector says a great deal about what a
// photo shows - only while the user has it on (settings.smart_search_enabled):
// turning it off deletes every row. See db.sql's photo_embeddings.

// SetPhotoEmbedding records hash's sealed vector, made by model, replacing
// any it had. ErrSchemaBehind without the table (the release's script
// hasn't run).
func (dao *Dao) SetPhotoEmbedding(hash, model string, sealed []byte) error {
	_, err := dao.db.Exec("insert into `photo_embeddings` (`hash`, `model`, `vec`, `made`) values (?, ?, ?, ?)"+
		" on duplicate key update `model` = values(`model`), `vec` = values(`vec`), `made` = values(`made`)",
		hash, model, sealed, time.Now().UTC())
	return schemaBehindTable(err)
}

// DelPhotoEmbedding forgets the vector of content nothing uses any more.
// Without the table there is nothing to forget.
func (dao *Dao) DelPhotoEmbedding(hash string) error {
	_, err := dao.db.Exec("delete from `photo_embeddings` where `hash` = ?", hash)
	if isNoSuchTable(err) {
		return nil
	}
	return err
}

// EachPhotoEmbedding calls fn with every vector made by model, sealed, in
// no particular order; fn's error stops it and is returned.
// ErrSchemaBehind without the table.
func (dao *Dao) EachPhotoEmbedding(model string, fn func(hash string, sealed []byte) error) error {
	rows, err := dao.db.Query("select `hash`, `vec` from `photo_embeddings` where `model` = ?", model)
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

// PhotosToEmbed is the next limit photos, by content after afterHash in
// order, that have no vector made by model yet: what the backfill still
// has to describe. Only photos Images may show: a copy outside excluded
// (issue #192). One row per content, its smallest path and mime.
// ErrSchemaBehind without the table.
func (dao *Dao) PhotosToEmbed(afterHash string, limit int, model string, excluded []string) (files []*pb.File, err error) {
	where := " where (`e`.`hash` is null or `e`.`model` <> ?) and `f`.`mime` like 'image%' and `f`.`hash` > ?"
	args := []any{model, afterHash}
	if cond, condArgs := outsideFolders("`f`.`path`", excluded); cond != "" {
		where += " and " + cond
		args = append(args, condArgs...)
	}
	args = append(args, limit)
	rows, err := dao.db.Query(
		"select `f`.`hash`, min(`f`.`mime`), min(`f`.`created`), min(`f`.`modified`), min(`f`.`path`), min(`f`.`size`) from `files` as `f`"+
			" left join `photo_embeddings` as `e` on `e`.`hash` = `f`.`hash`"+where+
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

// SmartSearchEnabled is whether Smart search is on for this library
// (settings.smart_search_enabled, off unless the user turns it on).
// ErrSchemaBehind before the release that added it, which callers read as
// off.
func (dao *Dao) SmartSearchEnabled() (on bool, err error) {
	err = dao.db.QueryRow("select `smart_search_enabled` from `settings`").Scan(&on)
	return on, schemaBehind(err)
}

// SetSmartSearchEnabled turns Smart search on or off. Off deletes every
// vector in the same transaction: the switch never says off while one is
// kept.
func (dao *Dao) SetSmartSearchEnabled(on bool) error {
	if on {
		_, err := dao.db.Exec("update `settings` set `smart_search_enabled` = 1")
		return schemaBehind(err)
	}
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("update `settings` set `smart_search_enabled` = 0"); err != nil {
		tx.Rollback()
		return schemaBehind(err)
	}
	if _, err := tx.Exec("delete from `photo_embeddings`"); err != nil && !isNoSuchTable(err) {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
