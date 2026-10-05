// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"time"
)

// Issue #176: an account unused for six months is removed (the terms say
// so), after an email a month before. "Used" is a sign-in to the account
// page (accounts.last_seen) or any client reaching one of its devices
// through the bridge (devices.last_client_at) - an owner whose devices are
// in use never has to visit the account page.

// InactiveAccount is an account nothing has used since some date.
type InactiveAccount struct {
	ID, Email, Name string
	// WarnedAt is when it was told it will be removed; nil if not yet.
	WarnedAt *time.Time
}

// cInactiveSince matches an account unused since ?: no sign-in, and no
// device of it reached, from then on.
const cInactiveSince = "a.`last_seen` < ? and not exists (select 1 from `devices` d where d.`account_id` = a.`id` and d.`last_client_at` >= ?)"

// InactiveAccounts lists the accounts unused since before.
func (dao *Dao) InactiveAccounts(before time.Time) ([]InactiveAccount, error) {
	b := before.UTC()
	rows, err := dao.db.Query("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at` from `accounts` a where "+cInactiveSince, b, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InactiveAccount
	for rows.Next() {
		var a InactiveAccount
		var warned sql.NullTime
		if err := rows.Scan(&a.ID, &a.Email, &a.Name, &warned); err != nil {
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
// since it was warned, so a later spell of inactivity warns again.
func (dao *Dao) ClearUsedInactivityWarnings() error {
	_, err := dao.db.Exec("update `accounts` a set a.`inactivity_warned_at` = null where a.`inactivity_warned_at` is not null and (a.`last_seen` > a.`inactivity_warned_at` or exists (select 1 from `devices` d where d.`account_id` = a.`id` and d.`last_client_at` > a.`inactivity_warned_at`))")

	return err
}
