// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Issue #144: many relayed messages become one write per device and hour,
// with the totals; a write that fails is retried with the next flush.
func TestMetricsAreBatched(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithDB(db)
	// The first message of a device also touches devices.last_client_at
	// (at most once a minute).
	mock.ExpectExec("update `devices` set `last_client_at`").WillReturnResult(sqlmock.NewResult(0, 1))
	for i := 0; i < 3; i++ {
		d.RecordDeviceActivity("cala.off-the.cloud", 10, 100)
	}

	insert := "insert into `device_metrics`"
	mock.ExpectExec(insert).WithArgs("cala.off-the.cloud", sqlmock.AnyArg(), 3, 30, 300).WillReturnError(errors.New("primary away"))
	d.FlushMetrics()

	d.RecordDeviceActivity("cala.off-the.cloud", 1, 1)
	mock.ExpectExec(insert).WithArgs("cala.off-the.cloud", sqlmock.AnyArg(), 4, 31, 301).WillReturnResult(sqlmock.NewResult(0, 1))
	d.FlushMetrics()

	d.FlushMetrics() // nothing left: no write
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
