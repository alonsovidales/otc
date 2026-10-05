// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// A warning that could not be emailed is released, so the next pass tries
// again rather than removing the account a month later, unwarned.
func TestInactivityWarningReleasedWhenTheEmailFails(t *testing.T) {
	a, mock := testAccounts(t)
	mock.ExpectExec("update `accounts` a set a.`inactivity_warned_at` = null").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at` from `accounts` a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at"}).AddRow("acc1", "a@b.c", "A", nil))
	mock.ExpectExec("update `accounts` set `inactivity_warned_at` = \\? where `id` = \\? and `inactivity_warned_at` is null").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `accounts` set `inactivity_warned_at` = null where `id` = \\?").WithArgs("acc1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at` from `accounts` a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at"}))

	a.inactivityPass(time.Now(), func(to, subject, body string) error { return errDBDown }, nil)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
