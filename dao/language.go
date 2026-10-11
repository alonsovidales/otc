// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
)

// --- Localization (docs/i18n.md, release 118) ---
//
// settings.language is the language the user chose for every app ("" for
// Automatic: each app follows its own system language), set only by the
// owner's SetLanguage; settings.last_ui_language is the language of the
// app that last registered for pushes, written from the owner's push
// registrations. Pushes are written in the first, else the second, else
// English (push.ResolveLanguage).
//
// A binary can meet a database its release script hasn't reached (a
// per-user database the script skipped, a restore of an older backup):
// these return ErrSchemaBehind then, and their callers carry on with the
// defaults instead of failing a request or the start.

// ErrSchemaBehind is a query naming a column the database doesn't have
// yet: the binary is newer than its schema.
var ErrSchemaBehind = errors.New("the database predates this release's columns")

// isUnknownColumn is MySQL's 1054: a column the table doesn't have.
func isUnknownColumn(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1054
}

// schemaBehind wraps an unknown-column error in ErrSchemaBehind.
func schemaBehind(err error) error {
	if isUnknownColumn(err) {
		return fmt.Errorf("%w: %v", ErrSchemaBehind, err)
	}
	return err
}

// GetLanguageSettings reads the stored choice and the language of the app
// that last registered for pushes ("" when none).
func (dao *Dao) GetLanguageSettings() (language, lastUILanguage string, err error) {
	err = dao.db.QueryRow("select `language`, `last_ui_language` from `settings`").Scan(&language, &lastUILanguage)
	if err != nil {
		return "", "", schemaBehind(err)
	}
	return language, lastUILanguage, nil
}

// SetLanguage stores the user's choice. The compare with the value the
// app last saw is the caller's (settings.SetLanguage, under its lock: one
// process owns each database).
func (dao *Dao) SetLanguage(language string) error {
	_, err := dao.db.Exec("update `settings` set `language` = ?", language)
	return schemaBehind(err)
}

// SetLastUILanguage stores the language of the app that last registered
// for pushes.
func (dao *Dao) SetLastUILanguage(lang string) error {
	_, err := dao.db.Exec("update `settings` set `last_ui_language` = ?", lang)
	return schemaBehind(err)
}
