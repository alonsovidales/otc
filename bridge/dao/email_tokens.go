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

// EmailTokenAccount is the account a live link of purpose belongs to,
// without using it up; ok is false for an unknown, used or expired link.
// A reset checks its link with it before hashing the new password, so a
// link that doesn't work costs no bcrypt.
func (dao *Dao) EmailTokenAccount(hash, purpose string) (accountID string, ok bool, err error) {
	err = dao.db.QueryRow("select `account_id` from `account_email_tokens` where `token_hash` = ? and `purpose` = ? and `expires` > ?",
		hash, purpose, time.Now().UTC()).Scan(&accountID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return accountID, true, nil
}

// lockEmailToken is the account a live link belongs to, read in tx with
// the link's row locked until tx ends: whoever deletes it is the one who
// used it (two requests with one link at the same moment). ok is false for
// an unknown, used or expired link.
func lockEmailToken(tx *sql.Tx, hash, purpose string) (accountID string, ok bool, err error) {
	err = tx.QueryRow("select `account_id` from `account_email_tokens` where `token_hash` = ? and `purpose` = ? and `expires` > ? for update",
		hash, purpose, time.Now().UTC()).Scan(&accountID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return accountID, true, nil
}

// VerifyEmailWithToken proves the email of the account a live link of
// purpose (a verification link) belongs to, and uses the link up - the
// account's other links of that purpose with it, which have nothing left
// to prove - in one transaction: a link is spent only once its account is
// marked verified, never before a write that may still fail. ok is false
// for an unknown, used or expired link, or one whose account is gone.
func (dao *Dao) VerifyEmailWithToken(hash, purpose string) (accountID string, ok bool, err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	accountID, ok, err = lockEmailToken(tx, hash, purpose)
	if err != nil || !ok {
		return "", false, err
	}
	// Its account still there: an update that changes nothing (the email
	// proven already) counts no rows either.
	var found int
	if err := tx.QueryRow("select 1 from `accounts` where `id` = ?", accountID).Scan(&found); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	if _, err := tx.Exec("update `accounts` set `email_verified` = 1 where `id` = ?", accountID); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec("delete from `account_email_tokens` where `account_id` = ? and `purpose` = ?", accountID, purpose); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return accountID, true, nil
}

// ResetPasswordWithToken sets passwordHash as the password of the account
// a live link of purpose (a reset link) belongs to, proves its email, ends
// every session of it - returning the new epoch, as
// SetAccountPasswordEndingSessions does - and uses the link up, with the
// account's other links of that purpose, all in one transaction: a
// password that isn't stored leaves the link working for the retry, and a
// password set leaves no older reset link that could set another. ok is
// false for an unknown, used or expired link, or one whose account is
// gone.
func (dao *Dao) ResetPasswordWithToken(hash, purpose, passwordHash string) (accountID string, epoch int, ok bool, err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return "", 0, false, err
	}
	defer tx.Rollback()
	accountID, ok, err = lockEmailToken(tx, hash, purpose)
	if err != nil || !ok {
		return "", 0, false, err
	}
	res, err := tx.Exec("update `accounts` set `password_hash` = ?, `email_verified` = 1, `session_epoch` = LAST_INSERT_ID(`session_epoch` + 1) where `id` = ?",
		passwordHash, accountID)
	if err != nil {
		return "", 0, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return "", 0, false, err
	}
	last, err := res.LastInsertId()
	if err != nil {
		return "", 0, false, err
	}
	if _, err := tx.Exec("delete from `account_email_tokens` where `account_id` = ? and `purpose` = ?", accountID, purpose); err != nil {
		return "", 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return "", 0, false, err
	}
	return accountID, int(last), true, nil
}

// PruneEmailTokens drops expired links.
func (dao *Dao) PruneEmailTokens() error {
	_, err := dao.db.Exec("delete from `account_email_tokens` where `expires` < ?", time.Now().UTC())
	return err
}
