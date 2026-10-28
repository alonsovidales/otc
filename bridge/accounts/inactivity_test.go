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
	mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at", "lang"}).AddRow("acc1", "a@b.c", "A", nil, ""))
	mock.ExpectExec("update `accounts` set `inactivity_warned_at` = \\? where `id` = \\? and `inactivity_warned_at` is null").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `accounts` set `inactivity_warned_at` = null where `id` = \\?").WithArgs("acc1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at", "lang"}))

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
		mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at", "lang"}).AddRow("acc1", "a@b.c", "A", nil, ""))
		mock.ExpectExec("update `accounts` set `inactivity_warned_at` = \\? where `id` = \\? and `inactivity_warned_at` is null").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at", "lang"}))
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

// A device connected to the bridge right now is use, whatever the stamps
// say (a database without devices.last_online_at, a stamp that failed to
// be written): its account is neither warned nor removed, and a warning
// it had is forgotten. An account whose devices can't be listed is left
// alone until the next pass.
func TestInactivityLeavesAccountsWithADeviceOnline(t *testing.T) {
	a, mock := testAccounts(t)
	var asked []string
	a.deviceOnline = func(domain string) bool {
		asked = append(asked, domain)
		return domain == "home.otc"
	}
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	cols := []string{"id", "email", "name", "inactivity_warned_at", "lang"}
	domains := func(names ...string) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"domain", "created", "disabled"})
		for _, n := range names {
			rows.AddRow(n, now, false)
		}
		return rows
	}
	const listDomains = "select `domain`, `created`, `disabled` from `devices` where `account_id` = \\?"
	const unmark = "update `accounts` set `inactivity_warned_at` = null where `id` = \\?"

	mock.ExpectExec("update `accounts` a set a.`inactivity_warned_at` = null").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("online", "online@b.c", "A", nil, "").
			AddRow("warned-online", "warned-online@b.c", "A", old, "").
			AddRow("unknown", "unknown@b.c", "A", nil, "").
			AddRow("offline", "offline@b.c", "A", nil, ""))
	mock.ExpectQuery(listDomains).WithArgs("online").WillReturnRows(domains("away.otc", "home.otc"))
	mock.ExpectQuery(listDomains).WithArgs("warned-online").WillReturnRows(domains("home.otc"))
	mock.ExpectExec(unmark).WithArgs("warned-online").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(listDomains).WithArgs("unknown").WillReturnError(errDBDown)
	mock.ExpectQuery(listDomains).WithArgs("offline").WillReturnRows(domains("away.otc"))
	mock.ExpectExec("update `accounts` set `inactivity_warned_at` = \\? where `id` = \\? and `inactivity_warned_at` is null").
		WithArgs(sqlmock.AnyArg(), "offline").WillReturnResult(sqlmock.NewResult(0, 1))
	// Due for removal, warned a month ago, its device back on today.
	mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
		WillReturnRows(sqlmock.NewRows(cols).AddRow("due-online", "due-online@b.c", "A", old, ""))
	mock.ExpectQuery(listDomains).WithArgs("due-online").WillReturnRows(domains("home.otc"))
	mock.ExpectExec(unmark).WithArgs("due-online").WillReturnResult(sqlmock.NewResult(0, 1))

	var sent []string
	removed := false
	a.inactivityPass(now, func(to, subject, body string) error { sent = append(sent, to); return nil },
		func([]string) { removed = true })
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0] != "offline@b.c" {
		t.Errorf("warnings sent to %v, want only offline@b.c", sent)
	}
	if removed {
		t.Error("an account with a device online was removed")
	}
	if want := []string{"away.otc", "home.otc", "home.otc", "away.otc", "home.otc"}; strings.Join(asked, " ") != strings.Join(want, " ") {
		t.Errorf("asked about %v, want %v", asked, want)
	}
}
