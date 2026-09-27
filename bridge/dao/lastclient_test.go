// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Issue #139: relayed traffic records the device's last client, but at
// most once a minute per domain - not a devices-row write per request.
func TestTouchLastClientThrottled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectExec("update `devices` set `last_client_at`").WithArgs(sqlmock.AnyArg(), "a.test").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `devices` set `last_client_at`").WithArgs(sqlmock.AnyArg(), "b.test").WillReturnResult(sqlmock.NewResult(0, 1))

	d.touchLastClient("a.test")
	d.touchLastClient("a.test") // within the minute: no query
	d.touchLastClient("b.test") // another domain: its own minute

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
