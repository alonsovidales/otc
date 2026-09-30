// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import "strings"

// Issue #173: files under a folder are found with the index on `path`, not
// with `path regexp '^folder/...'` - MariaDB can't use an index for a
// REGEXP, so every listing read the whole files table (and the desktop
// clients list their folders every minute), and the folder name went into
// the pattern unescaped: "Photos (2020)" or "a+b" matched the wrong files.
//
// A plain `path like 'folder/%'` isn't enough either: with the column's
// accent- and case-insensitive collation (utf8mb4_uca1400_ai_ci) MariaDB
// won't turn a LIKE into an index range when the prefix has characters
// like the ’ in "Alonso’s_MacBook_Air" - in every path a Mac syncs. An
// explicit range, from the prefix to the prefix followed by the highest
// code point, is one it always reads from the index; the LIKE stays as
// the exact test on the rows the range returns.

// likeEscape makes s match itself in a LIKE pattern (\ is LIKE's default
// escape character).
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// underPrefix is the WHERE condition for rows of col under prefix, with
// its arguments in order. direct keeps only the rows right in it, none
// in its sub-folders.
func underPrefix(col, prefix string, direct bool) (string, []any) {
	cond := col + " >= ? and " + col + " < concat(?, _utf8mb4 X'F48FBFBF') and " + col + " like ?"
	args := []any{prefix, prefix, likeEscape(prefix) + "%"}
	if direct {
		cond += " and " + col + " not like ?"
		args = append(args, likeEscape(prefix)+"%/%")
	}
	return cond, args
}
