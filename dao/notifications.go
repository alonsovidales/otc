// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/devtext"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// The alerts the device writes itself into notifications: errors (issue
// #64: grouped, or standalone), the mirror's disks and updates (issue
// #183). Each is written in English - title and details, which every app
// shows as they are - and, since release 118, as catalog keys too:
// msg_key and msg_args for the title, msg_lines for the details
// (alerttext), which LocalizeNotifications renders in a request's
// language. An alert with only its English (alerttext.English) writes no
// keys, and a row whose keys can't follow its English any more (an error
// with only its English joined it) loses them: that row shows its English
// in every language, never a mix. On a database without the columns
// (MySQL 1054, see ErrSchemaBehind) the English is written alone, as
// before.

// AddUpdateNotification (issue #183) tells the owner about a major or
// critical update, once per version: a notification whose update_version
// is version, or whose title is one of legacyTitles (the English titles
// naming the version: all that rows written before release 118 carry), is
// not added again. added says whether this call added it, so a push that
// goes with it is sent once too, across restarts. On a database without
// update_version (ErrSchemaBehind) it de-duplicates by the titles alone,
// as before.
func (dao *Dao) AddUpdateNotification(version string, a alerttext.Alert, legacyTitles []string) (added bool, err error) {
	if len(legacyTitles) == 0 {
		legacyTitles = []string{a.Title}
	}
	version = truncateUTF8(version, cUpdateVersionLen)
	in := "?" + strings.Repeat(", ?", len(legacyTitles)-1)
	args := []any{version}
	for _, t := range legacyTitles {
		args = append(args, t)
	}
	withVersion := true
	var n int
	err = dao.db.QueryRow("select count(*) from `notifications` where `type` = 'Update' and (`update_version` = ? or `title` in ("+in+"))", args...).Scan(&n)
	if isUnknownColumn(err) {
		withVersion = false
		err = dao.db.QueryRow("select count(*) from `notifications` where `type` = 'Update' and `title` in ("+in+")", args[1:]...).Scan(&n)
	}
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if withVersion {
		// update_version and the msg columns came with the same release.
		key, msgArgs, lines := msgColumns(a, a.DetailLine())
		_, err = dao.db.Exec("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`, `update_version`, `msg_key`, `msg_args`, `msg_lines`) values (?, now(), 'Update', 'This device', '', ?, ?, 1, ?, ?, ?, ?)",
			uuid.New(), a.Title, a.Detail, version, key, msgArgs, lines)
		if !isUnknownColumn(err) {
			return err == nil, err
		}
	}
	if _, err := dao.db.Exec("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`) values (?, now(), 'Update', 'This device', '', ?, ?, 1)",
		uuid.New(), a.Title, a.Detail); err != nil {
		return false, err
	}
	return true, nil
}

// cUpdateVersionLen is what notifications.update_version holds.
const cUpdateVersionLen = 16

// AddStorageNotification tells the owner a disk of the mirror stopped
// working, or that the mirror is whole again (raidwatch): an Error row of
// its own, never grouped with other errors, so it isn't buried in them.
// Its details are the detail alone.
func (dao *Dao) AddStorageNotification(a alerttext.Alert) error {
	if key, msgArgs, lines := msgColumns(a, a.DetailLine()); key.Valid {
		_, err := dao.db.Exec("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`, `msg_key`, `msg_args`, `msg_lines`) values (?, now(), 'Error', 'This device', '', ?, ?, 1, ?, ?, ?)",
			uuid.New(), a.Title, a.Detail, key, msgArgs, lines)
		if !isUnknownColumn(err) {
			return err
		}
	}
	_, err := dao.db.Exec("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`) values (?, now(), 'Error', 'This device', '', ?, ?, 1)",
		uuid.New(), a.Title, a.Detail)
	return err
}

// errorNotificationWindow is how long a group of errors stays open
// (issue #64): an error within this much of the group's first one joins
// it rather than adding a row of its own.
const errorNotificationWindow = "5 minute"

// errorNotificationDetailsCap is how much of an error group's details are
// kept, in bytes: the column is a TEXT (65,535), and a group that outgrew
// it failed every later error of its window. Past it, errors still count.
const errorNotificationDetailsCap = 60000

// AddErrorNotification (issue #64) records a device-side error - a photo
// that could not be processed, a file that never made it to disk - as an
// Error notification. Grouped so the list is never flooded: if an Error
// row was opened within the last five minutes this joins it (its line is
// appended to details, and to msg_lines, occurrences goes up, and the row
// is unread again); otherwise a new row starts with a's title as its
// one-liner. A standalone row (UpsertErrorNotification's,
// cStandaloneError) is never joined.
func (dao *Dao) AddErrorNotification(a alerttext.Alert) error {
	// One otc process owns each database (issue #82), so this is enough to
	// stop two first errors from each taking the gap lock of the select
	// below and then deadlocking (InnoDB 1213) on their inserts - one of
	// them lost.
	dao.errNotifMu.Lock()
	defer dao.errNotifMu.Unlock()

	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now()
	line := a.EnglishLine(now)
	const group = "from `notifications` where `type` = 'Error' and `actor_domain` = '' and `dt` >= now() - interval " + errorNotificationWindow + " order by `dt` desc limit 1 for update"
	var id string
	var size int
	var key, lines sql.NullString
	withMsg := true
	err = tx.QueryRow("select `uuid`, coalesce(length(`details`), 0), `msg_key`, `msg_lines` "+group).Scan(&id, &size, &key, &lines)
	if isUnknownColumn(err) {
		withMsg = false
		err = tx.QueryRow("select `uuid`, coalesce(length(`details`), 0) "+group).Scan(&id, &size)
	}
	switch {
	case err == nil && size+1+len(line) <= errorNotificationDetailsCap:
		change, joined := joinedLines(withMsg, key, lines, a, now)
		err = joinErrorGroup(tx, id, line, change, joined)
	case err == nil:
		_, err = tx.Exec("update `notifications` set `occurrences` = `occurrences` + 1, `acknowledged` = 0 where `uuid` = ?", id)
	case err == sql.ErrNoRows:
		if !withMsg {
			a = alerttext.English(a.Title, a.Detail) // no columns to try
		}
		err = insertError(tx, uuid.New().String(), "", a, truncateUTF8(line, errorNotificationDetailsCap), now)
	}
	if err != nil {
		return err
	}

	return tx.Commit()
}

// What a row's msg columns become when a line joins it.
type lineChange int

const (
	// linesKept: they stay as they are - a row with only its English, a
	// database without them, or msg_lines full.
	linesKept lineChange = iota
	// linesAppended: msg_lines takes the new line.
	linesAppended
	// linesDropped: the new line has only its English, so the row's keys
	// can't follow its details any more and go.
	linesDropped
)

// joinedLines decides what a joining line does to a row's msg columns
// (key and lines as the row has them), and returns msg_lines with the
// line added when it is added.
func joinedLines(withMsg bool, key, lines sql.NullString, a alerttext.Alert, now time.Time) (lineChange, []byte) {
	switch {
	case !withMsg || !key.Valid:
		return linesKept, nil
	case !a.Keyed():
		return linesDropped, nil
	}
	if raw, ok := alerttext.AppendLine([]byte(lines.String), a.Line(now)); ok {
		return linesAppended, raw
	}
	return linesKept, nil
}

// joinErrorGroup appends line to row id's details, and to its msg_lines
// as change says.
func joinErrorGroup(tx *sql.Tx, id, line string, change lineChange, lines []byte) error {
	const set = "update `notifications` set `details` = concat(coalesce(`details`, ''), '\n', ?), `occurrences` = `occurrences` + 1, `acknowledged` = 0"
	var err error
	switch change {
	case linesAppended:
		_, err = tx.Exec(set+", `msg_lines` = ? where `uuid` = ?", line, string(lines), id)
	case linesDropped:
		_, err = tx.Exec(set+", `msg_key` = null, `msg_args` = null, `msg_lines` = null where `uuid` = ?", line, id)
	default:
		_, err = tx.Exec(set+" where `uuid` = ?", line, id)
	}
	return err
}

// execer is a *sql.DB or a *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertError starts an Error row: grouped (actorDomain "") or standalone
// (cStandaloneError), whose details are its first line.
func insertError(ex execer, id, actorDomain string, a alerttext.Alert, details string, now time.Time) error {
	if key, msgArgs, lines := msgColumns(a, a.Line(now)); key.Valid {
		_, err := ex.Exec("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`, `msg_key`, `msg_args`, `msg_lines`) values (?, now(), 'Error', 'This device', ?, ?, ?, 1, ?, ?, ?)",
			id, actorDomain, a.Title, details, key, msgArgs, lines)
		if !isUnknownColumn(err) {
			return err
		}
	}
	_, err := ex.Exec("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`) values (?, now(), 'Error', 'This device', ?, ?, ?, 1)",
		id, actorDomain, a.Title, details)
	return err
}

// msgColumns are a's msg_key, msg_args and msg_lines (holding lines): all
// NULL for an alert with only its English, or whose lines don't fit
// msg_lines.
func msgColumns(a alerttext.Alert, lines ...alerttext.Line) (key, args, stored sql.NullString) {
	if !a.Keyed() {
		return
	}
	raw, ok := alerttext.Lines(lines...)
	if !ok {
		return
	}
	return nullString(a.Key), nullString(string(a.Args)), nullString(string(raw))
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// cStandaloneError marks, in actor_domain, an Error row no other error
// joins (AddErrorNotification groups only rows with an empty one). Not a
// domain anything could have - friends' notifications carry theirs, and
// friend data is deleted by it - and the clients show actor_name ("This
// device") instead.
const cStandaloneError = "(standalone)"

// UpsertErrorNotification is AddErrorNotification for an error that is one
// story however often it recurs - a device that keeps running out of
// memory - or that must not be buried in others' details (content set
// aside): the row id names, while it is there, takes a's title (which
// keeps the count), a line of details and one more occurrence, and is
// unread again; otherwise a standalone row starts (no grouped error joins
// it), with a's detail on its first line. It returns the row's id, for the
// next time.
func (dao *Dao) UpsertErrorNotification(id string, a alerttext.Alert) (string, error) {
	dao.errNotifMu.Lock()
	defer dao.errNotifMu.Unlock()

	now := time.Now()
	line := a.EnglishLine(now)
	if id != "" {
		updated, err := dao.updateStandalone(id, a, line, now)
		if err != nil {
			return "", err
		}
		if updated {
			return id, nil
		}
	}
	newID := uuid.New().String()
	if err := insertError(dao.db, newID, cStandaloneError, a, truncateUTF8(line, errorNotificationDetailsCap), now); err != nil {
		return "", err
	}
	return newID, nil
}

// updateStandalone is UpsertErrorNotification's update of row id; false
// when the row is gone (the owner deleted it).
func (dao *Dao) updateStandalone(id string, a alerttext.Alert, line string, now time.Time) (bool, error) {
	var size int
	var key, lines sql.NullString
	err := dao.db.QueryRow("select coalesce(length(`details`), 0), `msg_key`, `msg_lines` from `notifications` where `uuid` = ? and `type` = 'Error'", id).Scan(&size, &key, &lines)
	switch {
	case isUnknownColumn(err):
		res, err := dao.db.Exec("update `notifications` set `title` = ?, `details` = if(coalesce(length(`details`), 0) + 1 + ? <= ?, concat(coalesce(`details`, ''), '\n', ?), `details`), `occurrences` = `occurrences` + 1, `acknowledged` = 0 where `uuid` = ? and `type` = 'Error'",
			a.Title, len(line), errorNotificationDetailsCap, line, id)
		return affected(res, err)
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	}
	// The title is a's now, so the keys are too - unless the row has only
	// its English (written so, or by a binary from before keys): its keys
	// couldn't hold the lines before this one.
	newKey, newArgs, newLines := sql.NullString{}, sql.NullString{}, sql.NullString{}
	if a.Keyed() && key.Valid {
		newKey, newArgs, newLines = nullString(a.Key), nullString(string(a.Args)), lines
	}
	details := "`details`"
	args := []any{a.Title}
	if size+1+len(line) <= errorNotificationDetailsCap {
		details = "concat(coalesce(`details`, ''), '\n', ?)"
		args = append(args, line)
		if newKey.Valid {
			if raw, ok := alerttext.AppendLine([]byte(lines.String), a.Line(now)); ok {
				newLines = nullString(string(raw))
			}
		}
	}
	args = append(args, newKey, newArgs, newLines, id)
	res, err := dao.db.Exec("update `notifications` set `title` = ?, `details` = "+details+", `occurrences` = `occurrences` + 1, `acknowledged` = 0, `msg_key` = ?, `msg_args` = ?, `msg_lines` = ? where `uuid` = ? and `type` = 'Error'", args...)
	return affected(res, err)
}

// affected is whether an update found its row.
func affected(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return err == nil && n > 0, nil
}

// cLocalizeBatch is how many rows one LocalizeNotifications query asks for.
const cLocalizeBatch = 200

// LocalizeNotifications puts the alerts among notifications (as
// ListNotifications returned them) in lang: each title and details the
// device wrote with their keys is rendered from them, and anything that
// doesn't match the catalog keeps its English (alerttext.Render). A
// request without a language, or in English, is left exactly as it is,
// without a query. ErrSchemaBehind on a database without the columns:
// everything stays English.
func (dao *Dao) LocalizeNotifications(lang string, notifications []*pb.Notification) error {
	if devtext.English(lang) {
		return nil
	}
	byID := map[string]*pb.Notification{}
	var ids []any
	for _, n := range notifications {
		if n.Type == pb.NotificationType_NotificationError || n.Type == pb.NotificationType_NotificationUpdate {
			byID[n.Uuid] = n
			ids = append(ids, n.Uuid)
		}
	}
	for len(ids) > 0 {
		batch := ids[:min(len(ids), cLocalizeBatch)]
		ids = ids[len(batch):]
		if err := dao.localizeBatch(lang, batch, byID); err != nil {
			return err
		}
	}
	return nil
}

func (dao *Dao) localizeBatch(lang string, ids []any, byID map[string]*pb.Notification) error {
	rows, err := dao.db.Query("select `uuid`, `msg_key`, `msg_args`, `msg_lines` from `notifications` where `msg_key` is not null and `uuid` in (?"+strings.Repeat(", ?", len(ids)-1)+")", ids...)
	if err != nil {
		return schemaBehind(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, key string
		var args, lines sql.NullString
		if err := rows.Scan(&id, &key, &args, &lines); err != nil {
			return err
		}
		if n := byID[id]; n != nil {
			n.Title, n.Details = alerttext.Render(lang, key, []byte(args.String), []byte(lines.String), n.Title, n.Details)
		}
	}
	return rows.Err()
}
