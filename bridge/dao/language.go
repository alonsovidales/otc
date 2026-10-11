// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/alonsovidales/otc/log"
)

// Localization (docs/i18n.md): the language the bridge writes in, kept in
// two columns that migration 010-language.sql adds -
//
//   - accounts.lang: the account's emails. Set at sign-up from the page or
//     the browser, changed only by the signed-in owner.
//   - push_registrations.language: the bridge's own pushes to a device's
//     phones and browsers (the device-offline alert), as the device last
//     reported it in UpdatePushRegistrations.
//
// "" in either is "not known" (English). The migration is run by hand
// before the bridge that uses it is deployed, but a node started before
// it, or a database restored from an older dump, must go on answering:
// every query that names one of them falls back to the same query without
// it (optionalColumn), so sign-ups, sign-ins and push registrations never
// fail over a missing column - the language is simply not kept.
//
// Never log a language next to a domain or an account: it is a preference
// of a person, and the logs are kept with neither in mind.

// ErrNoLanguageColumn is SetAccountLang on a database without
// accounts.lang (migration 010 not run): there is nowhere to keep it.
var ErrNoLanguageColumn = errors.New("the database has no language column yet (bridge/db/migrations/010-language.sql)")

// cColumnRecheck is how long a column found missing is left out before a
// query tries it again, so running the migration under a live bridge
// needs no restart.
const cColumnRecheck = 5 * time.Minute

// optionalColumn is a column this binary can run without. The zero value
// is "there, as far as we know".
type optionalColumn struct {
	// missingSince is when a query last found the column missing (unix
	// nanoseconds); 0 while it is there.
	missingSince atomic.Int64
}

// run runs withCol, unless the column was found missing less than
// cColumnRecheck ago, and withoutCol instead when it is known missing or
// withCol fails because it is (MySQL's "unknown column", 1054, naming
// column). Any other error is withCol's own. withCol must have changed
// nothing when it fails that way, which holds: MySQL refuses the
// statement before running it, and a transaction it was part of goes on.
func (c *optionalColumn) run(table, column string, withCol, withoutCol func() error) error {
	if since := c.missingSince.Load(); since == 0 || time.Now().UnixNano()-since >= int64(cColumnRecheck) {
		err := withCol()
		if !isUnknownColumn(err, column) {
			if since != 0 && (err == nil || errors.Is(err, sql.ErrNoRows)) && c.missingSince.CompareAndSwap(since, 0) {
				log.Info("dao: the column", table+"."+column, "is there now")
			}
			return err
		}
		if c.missingSince.Swap(time.Now().UnixNano()) == 0 {
			log.Error("dao: the column", table+"."+column, "is missing - run bridge/db/migrations/010-language.sql; going on without it")
		}
	}
	return withoutCol()
}

// isUnknownColumn is whether err is MySQL refusing a statement because it
// names column and the table has no such column (ER_BAD_FIELD_ERROR). The
// message names the column as 'lang' or 'a.lang'; the text check covers
// errors that only carry the text, as IsDuplicateKey does.
func isUnknownColumn(err error, column string) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1054 && namesColumn(me.Message, column)
	}
	return strings.Contains(err.Error(), "Unknown column") && namesColumn(err.Error(), column)
}

// namesColumn: msg quotes column, bare ('lang') or qualified ('a.lang').
func namesColumn(msg, column string) bool {
	return strings.Contains(msg, "'"+column+"'") || strings.Contains(msg, "."+column+"'")
}

// SetAccountLang sets the language the account's emails are written in
// ("" for not known). The caller validates it (i18n.Normalize).
func (dao *Dao) SetAccountLang(accountID, lang string) error {
	return dao.accountLang.run("accounts", "lang", func() error {
		_, err := dao.db.Exec("update `accounts` set `lang` = ? where `id` = ?", lang, accountID)
		return err
	}, func() error { return ErrNoLanguageColumn })
}

// PushLanguageForDomain is the language domain's device last reported for
// the bridge's own pushes, as stored: "" when it reported none, the domain
// has no registrations, or the column is missing. The caller normalizes it
// (i18n.Normalize) before rendering with it.
func (dao *Dao) PushLanguageForDomain(domain string) (lang string, err error) {
	err = dao.pushLanguage.run("push_registrations", "language", func() error {
		return dao.db.QueryRow("select `language` from `push_registrations` where `domain` = ?", domain).Scan(&lang)
	}, func() error { lang = ""; return nil })
	if err == sql.ErrNoRows {
		return "", nil
	}

	return lang, err
}
