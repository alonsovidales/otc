// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
	"time"

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

// A primary that stops answering costs a flush its time limit, not a hang
// (Stop waits for the last flush): the counts it couldn't write, and the
// ones it never got to, are kept for the next one.
func TestMetricsFlushIsBounded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectExec("update `devices` set `last_client_at`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `devices` set `last_client_at`").WillReturnResult(sqlmock.NewResult(0, 1))
	d.RecordDeviceActivity("a.off-the.cloud", 1, 2)
	d.RecordDeviceActivity("b.off-the.cloud", 3, 4)

	mock.ExpectExec("insert into `device_metrics`").WillDelayFor(time.Minute).WillReturnResult(sqlmock.NewResult(0, 1))
	start := time.Now()
	d.flushMetrics(100 * time.Millisecond)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("a stalled flush took %v", took)
	}
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	if len(d.metrics) != 2 {
		t.Fatalf("%d of 2 counts kept", len(d.metrics))
	}
	for k, c := range d.metrics {
		if c.requests != 1 || c.in+c.out == 0 {
			t.Errorf("%s: %+v", k.domain, *c)
		}
	}
}
