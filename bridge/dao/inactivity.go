// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"time"
)

// Issue #176: an account unused for six months is removed (the terms say
// so), after an email a month before. "Used" is a sign-in to the account
// page (accounts.last_seen), any client reaching one of its devices
// through the bridge (devices.last_client_at), or one of its devices
// connected to the bridge (devices.last_online_at, lastonline.go) - an
// owner whose devices are on never has to visit the account page, even
// when they are used only at home or over Tailscale, where nothing passes
// through the bridge.

// InactiveAccount is an account nothing has used since some date.
type InactiveAccount struct {
	ID, Email, Name string
	// WarnedAt is when it was told it will be removed; nil if not yet.
	WarnedAt *time.Time
	// Lang is the account's language for emails (Account.Lang).
	Lang string
}

// cInactiveSince matches an account unused since ?: no sign-in, and no
// device of it reached or connected, from then on. It takes the date
// three times.
const cInactiveSince = "a.`last_seen` < ? and not exists (select 1 from `devices` d where d.`account_id` = a.`id` and (d.`last_client_at` >= ? or d.`last_online_at` >= ?))"

// cInactiveSinceNoOnline is cInactiveSince on a database without
// devices.last_online_at (migration 012 not run), taking the date twice.
const cInactiveSinceNoOnline = "a.`last_seen` < ? and not exists (select 1 from `devices` d where d.`account_id` = a.`id` and d.`last_client_at` >= ?)"

// InactiveAccounts lists the accounts unused since before, each with its
// Lang unless the database has no accounts.lang (language.go). On a
// database without devices.last_online_at, a device connected counts for
// nothing here: the caller checks the live connections too.
func (dao *Dao) InactiveAccounts(before time.Time) (out []InactiveAccount, err error) {
	b := before.UTC()
	err = dao.lastOnlineCol.run("devices", "last_online_at", func() (err error) {
		out, err = dao.inactiveAccountsLang(cInactiveSince, b, b, b)
		return err
	}, func() (err error) {
		out, err = dao.inactiveAccountsLang(cInactiveSinceNoOnline, b, b)
		return err
	})

	return out, err
}

// inactiveAccountsLang is the accounts matching where, with their
// language unless the database has no accounts.lang.
func (dao *Dao) inactiveAccountsLang(where string, args ...any) (out []InactiveAccount, err error) {
	err = dao.accountLang.run("accounts", "lang", func() (err error) {
		out, err = dao.inactiveAccounts(", a.`lang`", where, args...)
		return err
	}, func() (err error) {
		out, err = dao.inactiveAccounts("", where, args...)
		return err
	})

	return out, err
}

// inactiveAccounts is the accounts matching where, reading the columns
// plus extra (", a.`lang`" or nothing).
func (dao *Dao) inactiveAccounts(extra, where string, args ...any) ([]InactiveAccount, error) {
	rows, err := dao.db.Query("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`"+extra+" from `accounts` a where "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InactiveAccount
	for rows.Next() {
		var a InactiveAccount
		var warned sql.NullTime
		dest := []any{&a.ID, &a.Email, &a.Name, &warned}
		if extra != "" {
			dest = append(dest, &a.Lang)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if warned.Valid {
			t := warned.Time
			a.WarnedAt = &t
		}
		out = append(out, a)
	}

	return out, rows.Err()
}

// MarkInactivityWarned records the warning, once: false when it was already
// recorded (the other cluster node got there first), so only one email
// goes out.
func (dao *Dao) MarkInactivityWarned(accountID string, at time.Time) (bool, error) {
	res, err := dao.db.Exec("update `accounts` set `inactivity_warned_at` = ? where `id` = ? and `inactivity_warned_at` is null", at.UTC(), accountID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()

	return n == 1, err
}

// UnmarkInactivityWarned forgets a warning that could not be emailed, so
// the next pass tries again: an account is never removed unwarned. By id
// alone - the column holds whole seconds, so the claim's time wouldn't
// match - which is safe: while the claim is held, nothing else sets it.
func (dao *Dao) UnmarkInactivityWarned(accountID string) error {
	_, err := dao.db.Exec("update `accounts` set `inactivity_warned_at` = null where `id` = ?", accountID)
	return err
}

// ClearUsedInactivityWarnings forgets the warning of every account used
// since it was warned - signed in, a device of it reached or connected -
// so a later spell of inactivity warns again.
func (dao *Dao) ClearUsedInactivityWarnings() error {
	return dao.lastOnlineCol.run("devices", "last_online_at", func() error {
		_, err := dao.db.Exec("update `accounts` a set a.`inactivity_warned_at` = null where a.`inactivity_warned_at` is not null and (a.`last_seen` > a.`inactivity_warned_at` or exists (select 1 from `devices` d where d.`account_id` = a.`id` and (d.`last_client_at` > a.`inactivity_warned_at` or d.`last_online_at` > a.`inactivity_warned_at`)))")
		return err
	}, func() error {
		_, err := dao.db.Exec("update `accounts` a set a.`inactivity_warned_at` = null where a.`inactivity_warned_at` is not null and (a.`last_seen` > a.`inactivity_warned_at` or exists (select 1 from `devices` d where d.`account_id` = a.`id` and d.`last_client_at` > a.`inactivity_warned_at`))")
		return err
	})
}
