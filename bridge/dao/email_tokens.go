// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"time"
)

// Email verification and password-reset links. Only the token's SHA-256
// is stored (the token is in the email alone), each is single use, and a
// new one of the same purpose replaces the account's previous ones - once
// it has been sent: a send that fails leaves the link already in the
// inbox working.

// SetEmailVerified marks accountID's email as proven.
func (dao *Dao) SetEmailVerified(accountID string) error {
	_, err := dao.db.Exec("update `accounts` set `email_verified` = 1 where `id` = ?", accountID)
	return err
}

// AddEmailToken stores hash as a link of accountID's for purpose, next to
// any older ones (KeepNewestEmailToken retires those once it is sent). It
// returns when the link was made, as stored: to the second, the column's
// resolution.
func (dao *Dao) AddEmailToken(hash, accountID, purpose string, ttl time.Duration) (created time.Time, err error) {
	now := time.Now().UTC().Truncate(time.Second)
	_, err = dao.db.Exec("insert into `account_email_tokens` (`token_hash`, `account_id`, `purpose`, `created`, `expires`) values (?, ?, ?, ?, ?)",
		hash, accountID, purpose, now, now.Add(ttl))
	return now, err
}

// KeepNewestEmailToken drops accountID's links for purpose made before
// created, the time of hash's. Strictly before: two links made at once
// (two requests past the throttle together) both stay, rather than each
// deleting the other.
func (dao *Dao) KeepNewestEmailToken(hash, accountID, purpose string, created time.Time) error {
	_, err := dao.db.Exec("delete from `account_email_tokens` where `account_id` = ? and `purpose` = ? and `token_hash` <> ? and `created` < ?",
		accountID, purpose, hash, created.UTC())
	return err
}

// DropEmailToken removes one link (one whose email could not be sent).
func (dao *Dao) DropEmailToken(hash string) error {
	_, err := dao.db.Exec("delete from `account_email_tokens` where `token_hash` = ?", hash)
	return err
}

// LastEmailTokenSent is when accountID's current link for purpose was
// made; zero when there is none (for throttling resends).
func (dao *Dao) LastEmailTokenSent(accountID, purpose string) (time.Time, error) {
	var t sql.NullTime
	err := dao.db.QueryRow("select max(`created`) from `account_email_tokens` where `account_id` = ? and `purpose` = ?", accountID, purpose).Scan(&t)
	return t.Time, err
}

// ConsumeEmailToken returns the account a live link belongs to and deletes
// it; ok is false for an unknown, used or expired link.
func (dao *Dao) ConsumeEmailToken(hash, purpose string) (accountID string, ok bool, err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return "", false, err
	}
	err = tx.QueryRow("select `account_id` from `account_email_tokens` where `token_hash` = ? and `purpose` = ? and `expires` > ? for update",
		hash, purpose, time.Now().UTC()).Scan(&accountID)
	if err == sql.ErrNoRows {
		tx.Rollback()
		return "", false, nil
	}
	if err != nil {
		tx.Rollback()
		return "", false, err
	}
	if _, err := tx.Exec("delete from `account_email_tokens` where `token_hash` = ?", hash); err != nil {
		tx.Rollback()
		return "", false, err
	}
	return accountID, true, tx.Commit()
}

// PruneEmailTokens drops expired links.
func (dao *Dao) PruneEmailTokens() error {
	_, err := dao.db.Exec("delete from `account_email_tokens` where `expires` < ?", time.Now().UTC())
	return err
}
