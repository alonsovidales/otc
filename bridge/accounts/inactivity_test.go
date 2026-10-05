// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/bridge/mailer"
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

// An address the mail server refuses for good keeps its warning: retrying
// it every day would keep the account for ever. A refusal that may clear
// up, or that isn't about the address, is released like any failed send.
func TestInactivityWarningStandsWhenTheAddressIsRefused(t *testing.T) {
	refused := func(code int, msg string) error {
		return &mailer.RecipientError{Err: &textproto.Error{Code: code, Msg: msg}}
	}
	for _, c := range []struct {
		err     error
		release bool
	}{
		{refused(550, "5.1.1 <a@b.c>: Recipient address rejected: no such user"), false},
		{refused(553, "5.1.3 Bad recipient address syntax"), false},
		{refused(450, "4.1.1 <a@b.c>: Recipient address rejected: try again later"), true},
		{refused(550, "5.7.1 Daily sending limit exceeded"), true},
		{refused(553, "5.1.8 <info@off-the.cloud>: Sender address rejected"), true},
		{refused(550, "Requested action not taken"), true},
		{&textproto.Error{Code: 535, Msg: "5.7.8 Authentication failed"}, true},
	} {
		a, mock := testAccounts(t)
		// Unordered, so a release would meet the expectation below
		// rather than fail unseen as an unexpected statement.
		mock.MatchExpectationsInOrder(false)
		mock.ExpectExec("update `accounts` a set a.`inactivity_warned_at` = null").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at` from `accounts` a").
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at"}).AddRow("acc1", "a@b.c", "A", nil))
		mock.ExpectExec("update `accounts` set `inactivity_warned_at` = \\? where `id` = \\? and `inactivity_warned_at` is null").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at` from `accounts` a").
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at"}))
		mock.ExpectExec("update `accounts` set `inactivity_warned_at` = null where `id` = \\?").WithArgs("acc1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		a.inactivityPass(time.Now(), func(to, subject, body string) error { return c.err }, nil)
		err := mock.ExpectationsWereMet()
		released := err == nil
		if err != nil && !strings.Contains(err.Error(), "= null where `id`") {
			t.Fatalf("%v: %v", c.err, err)
		}
		if released != c.release {
			t.Errorf("%v: warning released = %v, want %v", c.err, released, c.release)
		}
	}
}
