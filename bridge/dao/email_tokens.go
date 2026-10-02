// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"time"
)

// Email verification and password-reset links. Only the token's SHA-256
// is stored (the token is in the email alone), each is single use, and a
// new one of the same purpose replaces the account's previous one.

// SetEmailVerified marks accountID's email as proven.
func (dao *Dao) SetEmailVerified(accountID string) error {
	_, err := dao.db.Exec("update `accounts` set `email_verified` = 1 where `id` = ?", accountID)
	return err
}

// SaveEmailToken stores hash as accountID's link for purpose, replacing an
// older one of the same purpose.
func (dao *Dao) SaveEmailToken(hash, accountID, purpose string, ttl time.Duration) error {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("delete from `account_email_tokens` where `account_id` = ? and `purpose` = ?", accountID, purpose); err != nil {
		tx.Rollback()
		return err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec("insert into `account_email_tokens` (`token_hash`, `account_id`, `purpose`, `created`, `expires`) values (?, ?, ?, ?, ?)",
		hash, accountID, purpose, now, now.Add(ttl)); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
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
