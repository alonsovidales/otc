// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

// How much each background pass has to go through when it starts
// (Status.background_tasks, release 152): the same rows its listing pages
// through, counted once by content hash.

// CountMediaFilesAfter is ListMediaForReprocess's rows after afterHash.
func (dao *Dao) CountMediaFilesAfter(afterHash string) (n int64, err error) {
	err = dao.db.QueryRow("select count(distinct `hash`) from `files` where (`mime` like 'image%' or `mime` like 'video%') and `hash` > ?", afterHash).Scan(&n)
	return n, err
}

// CountMediaWithoutLocation is MediaWithoutLocation's rows.
func (dao *Dao) CountMediaWithoutLocation() (n int64, err error) {
	err = dao.db.QueryRow(
		"select count(distinct `f`.`hash`) from `files` as `f`" +
			" left join `photo_locations` as `l` on `l`.`hash` = `f`.`hash`" +
			" where `l`.`hash` is null and (`f`.`mime` like 'image%' or `f`.`mime` like 'video%')").Scan(&n)
	return n, schemaBehindTable(err)
}

// CountPhotosToScore is PhotosToScore's rows.
func (dao *Dao) CountPhotosToScore(withoutAesthetic bool, excluded []string) (n int64, err error) {
	missing := "`q`.`hash` is null"
	if withoutAesthetic {
		missing = "(`q`.`hash` is null or `q`.`aesthetic` is null)"
	}
	where := " where " + missing + " and `f`.`mime` like 'image%'"
	var args []any
	if cond, condArgs := outsideFolders("`f`.`path`", excluded); cond != "" {
		where += " and " + cond
		args = append(args, condArgs...)
	}
	err = dao.db.QueryRow("select count(distinct `f`.`hash`) from `files` as `f`"+
		" left join `photo_quality` as `q` on `q`.`hash` = `f`.`hash`"+where, args...).Scan(&n)
	return n, schemaBehindTable(err)
}

// CountPhotosToEmbed is PhotosToEmbed's rows.
func (dao *Dao) CountPhotosToEmbed(model string, excluded []string) (n int64, err error) {
	where := " where (`e`.`hash` is null or `e`.`model` <> ?) and `f`.`mime` like 'image%'"
	args := []any{model}
	if cond, condArgs := outsideFolders("`f`.`path`", excluded); cond != "" {
		where += " and " + cond
		args = append(args, condArgs...)
	}
	err = dao.db.QueryRow("select count(distinct `f`.`hash`) from `files` as `f`"+
		" left join `photo_embeddings` as `e` on `e`.`hash` = `f`.`hash`"+where, args...).Scan(&n)
	return n, schemaBehindTable(err)
}
