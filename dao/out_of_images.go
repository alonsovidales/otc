// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// --- Issue #192: folders kept out of Images ---
//
// out_of_images_folders holds the flagged folders (paths with their
// trailing slash, like upload_only_folders), skipped_analysis the content
// whose tags and faces were never worked out, or were deleted, because
// every path holding it is under one of them. Content is "kept out" when
// some row of it - in files or file_versions - is under a flagged folder
// and none is outside them all; files_manager/out_of_images.go owns the
// rule, these are its queries.

// cHashChunk is how many hashes one statement names: a folder can hold
// tens of thousands of photos, and a statement with that many
// placeholders is slow to build and to plan.
const cHashChunk = 500

// isNoSuchTable is MySQL's 1146: the table doesn't exist (a binary newer
// than the schema its database has, before release 108's script).
func isNoSuchTable(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1146
}

// hashChunks splits hashes into slices of at most cHashChunk.
func hashChunks(hashes []string) [][]string {
	var out [][]string
	for start := 0; start < len(hashes); start += cHashChunk {
		out = append(out, hashes[start:min(start+cHashChunk, len(hashes))])
	}
	return out
}

// outermostFolders is folders without the ones inside another of them,
// sorted and without repeats: a folder inside a flagged one adds nothing
// to the condition that leaves the outer one out.
func outermostFolders(folders []string) []string {
	sorted := append([]string(nil), folders...)
	sort.Strings(sorted)
	var out []string
	for _, f := range sorted {
		// Sorted, so a folder containing f (a prefix of it) came before.
		if len(out) > 0 && strings.HasPrefix(f, out[len(out)-1]) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// outsideFolders is the WHERE condition for rows of col in none of
// folders, with its arguments in order; "" for none. underPrefix's exact
// range and binary LIKE, negated - right for "Photos (2020)", "a_b" and
// "Alonso’s_MacBook_Air". Folders inside another listed folder are
// dropped first (outermostFolders), so the condition has one term per
// tree.
func outsideFolders(col string, folders []string) (string, []any) {
	var parts []string
	var args []any
	for _, f := range outermostFolders(folders) {
		cond, condArgs := underPrefix(col, f, false)
		parts = append(parts, "not ("+cond+")")
		args = append(args, condArgs...)
	}
	return strings.Join(parts, " and "), args
}

// underFolders is outsideFolders' opposite: rows of col under any of
// folders; "" for none.
func underFolders(col string, folders []string) (string, []any) {
	var parts []string
	var args []any
	for _, f := range outermostFolders(folders) {
		cond, condArgs := underPrefix(col, f, false)
		parts = append(parts, "("+cond+")")
		args = append(args, condArgs...)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " or ") + ")", args
}

// GetOutOfImagesFolders lists the folders kept out of Images, each with
// its trailing slash. A database without the table (release 108's script
// hasn't reached it) has none: without the table there can be no flag.
func (dao *Dao) GetOutOfImagesFolders() (paths []string, err error) {
	rows, err := dao.db.Query("select `path` from `out_of_images_folders`")
	if isNoSuchTable(err) {
		return nil, nil
	}
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

// SetOutOfImagesFolder flags or clears a folder (path with trailing slash).
func (dao *Dao) SetOutOfImagesFolder(path string, on bool) (err error) {
	if on {
		_, err = dao.db.Exec("insert ignore into `out_of_images_folders` (`path`) values (?)", path)
	} else {
		_, err = dao.db.Exec("delete from `out_of_images_folders` where `path` = ?", path)
	}

	return err
}

// DelOutOfImagesUnder clears folder's flag and every flag under it - a
// deleted folder's (DelPath).
func (dao *Dao) DelOutOfImagesUnder(folder string) error {
	cond, args := underPrefix("`path`", folder, false)
	_, err := dao.db.Exec("delete from `out_of_images_folders` where "+cond, args...)
	if isNoSuchTable(err) {
		return nil
	}
	return err
}

// cMediaRows is isMedia's rule (files_manager/lanes.go) in SQL: what
// processing tags and searches for faces.
const cMediaRows = "(`mime` like 'image%' or `mime` like 'video/%' or `path` like '%.HEIC' collate utf8mb4_bin)"

// MediaHashesUnder is the content of every photo and video under folder,
// current files and kept versions alike, each hash once.
func (dao *Dao) MediaHashesUnder(folder string) (hashes []string, err error) {
	cond, args := underPrefix("`path`", folder, false)
	rows, err := dao.db.Query("select `hash` from `files` where "+cond+" and "+cMediaRows+
		" union select `hash` from `file_versions` where "+cond+" and "+cMediaRows, append(args, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}

	return hashes, rows.Err()
}

// VisibleHashes is which of hashes a file or kept version holds outside
// every one of folders - content Images may show and processing may
// analyse. With no folders, every hash some row still holds.
func (dao *Dao) VisibleHashes(hashes, folders []string) (map[string]bool, error) {
	cond, condArgs := outsideFolders("`path`", folders)
	if cond != "" {
		cond = " and " + cond
	}
	return dao.hashesWithRows(hashes, cond, condArgs)
}

// HashesWithRowsUnder is which of hashes a file or kept version holds
// under one of folders.
func (dao *Dao) HashesWithRowsUnder(hashes, folders []string) (map[string]bool, error) {
	cond, condArgs := underFolders("`path`", folders)
	if cond == "" {
		return map[string]bool{}, nil
	}
	return dao.hashesWithRows(hashes, " and "+cond, condArgs)
}

// hashesWithRows is which of hashes a row of files or file_versions
// matching cond (" and ..." or "") holds, cHashChunk at a time.
func (dao *Dao) hashesWithRows(hashes []string, cond string, condArgs []any) (map[string]bool, error) {
	found := map[string]bool{}
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		all := append(append(append(append([]any{}, args...), condArgs...), args...), condArgs...)
		rows, err := dao.db.Query("select `hash` from `files` where `hash` in ("+ph+")"+cond+
			" union select `hash` from `file_versions` where `hash` in ("+ph+")"+cond, all...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			found[h] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}

// DelTagsByHashes removes the tags of every one of hashes.
func (dao *Dao) DelTagsByHashes(hashes []string) error {
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		if _, err := dao.db.Exec("delete from `file_tags` where `hash` in ("+ph+")", args...); err != nil {
			return err
		}
	}
	return nil
}

// AddSkippedAnalysis records that hashes' analysis was skipped, or its
// result deleted, because they are kept out of Images.
func (dao *Dao) AddSkippedAnalysis(hashes []string) error {
	now := time.Now().UTC()
	for _, chunk := range hashChunks(hashes) {
		args := make([]any, 0, 2*len(chunk))
		for _, h := range chunk {
			args = append(args, h, now)
		}
		values := strings.TrimSuffix(strings.Repeat("(?, ?), ", len(chunk)), ", ")
		if _, err := dao.db.Exec("insert ignore into `skipped_analysis` (`hash`, `skipped`) values "+values, args...); err != nil {
			return err
		}
	}
	return nil
}

// TakeSkippedAnalysis moves the rows of hashes recorded as skipped to
// pending_analysis and returns those it found, in one transaction: the
// caller owes them an analysis, and should it stop before queueing them
// all, ResumePendingAnalysis finds the rest - never in neither table.
func (dao *Dao) TakeSkippedAnalysis(hashes []string) (taken []string, err error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	tx, err := dao.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		rows, err := tx.Query("select `hash` from `skipped_analysis` where `hash` in ("+ph+") for update", args...)
		if err != nil {
			return nil, err
		}
		var found []string
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			found = append(found, h)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			continue
		}
		pargs := make([]any, 0, 2*len(found))
		for _, h := range found {
			pargs = append(pargs, h, now)
		}
		values := strings.TrimSuffix(strings.Repeat("(?, ?), ", len(found)), ", ")
		if _, err := tx.Exec("insert ignore into `pending_analysis` (`hash`, `queued`) values "+values, pargs...); err != nil {
			return nil, err
		}
		fph, fargs := inPlaceholders(found)
		if _, err := tx.Exec("delete from `skipped_analysis` where `hash` in ("+fph+")", fargs...); err != nil {
			return nil, err
		}
		taken = append(taken, found...)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return taken, nil
}

// DelSkippedAnalysis forgets hashes' skipped analysis: their content is
// gone. Nothing to forget without the table.
func (dao *Dao) DelSkippedAnalysis(hashes ...string) error {
	for _, chunk := range hashChunks(hashes) {
		ph, args := inPlaceholders(chunk)
		_, err := dao.db.Exec("delete from `skipped_analysis` where `hash` in ("+ph+")", args...)
		if isNoSuchTable(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// SkippedAnalysis lists every hash recorded as skipped; none without the
// table.
func (dao *Dao) SkippedAnalysis() (hashes []string, err error) {
	rows, err := dao.db.Query("select `hash` from `skipped_analysis`")
	if isNoSuchTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}
