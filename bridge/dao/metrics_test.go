// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
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

// A primary that stops answering costs a flush its time limit and one
// write's, not a hang (Stop waits for the last flush). The write that got
// no answer may have been applied, so its counts are dropped; the ones the
// flush never got to are kept for the next one.
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
	d.flushMetrics(100*time.Millisecond, 200*time.Millisecond)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("a stalled flush took %v", took)
	}
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	if len(d.metrics) != 1 {
		t.Fatalf("%d counts kept, want only the one never written", len(d.metrics))
	}
	for k, c := range d.metrics {
		if c.requests != 1 || c.in+c.out == 0 {
			t.Errorf("%s: %+v", k.domain, *c)
		}
	}
}

// A write that may have been applied (the connection lost waiting for the
// answer) is not retried: its counts would be added twice. One MySQL
// refused (a lock wait timeout) was not applied, and is.
func TestMetricsWriteMaybeAppliedIsNotRetried(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectExec("update `devices` set `last_client_at`").WillReturnResult(sqlmock.NewResult(0, 1))
	insert := "insert into `device_metrics`"

	d.RecordDeviceActivity("a.off-the.cloud", 1, 2)
	mock.ExpectExec(insert).WillReturnError(mysql.ErrInvalidConn)
	d.flushMetrics(time.Minute, time.Minute)
	d.FlushMetrics() // nothing kept: no write

	d.RecordDeviceActivity("a.off-the.cloud", 1, 2)
	mock.ExpectExec(insert).WillReturnError(&mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded; try restarting transaction"})
	d.flushMetrics(time.Minute, time.Minute)
	mock.ExpectExec(insert).WithArgs("a.off-the.cloud", sqlmock.AnyArg(), 1, 1, 2).WillReturnResult(sqlmock.NewResult(0, 1))
	d.FlushMetrics()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A write still under way when the pass runs out of time is not cut short:
// it may already be applied, and its counts, kept, would be added twice.
// Only the writes not started yet wait for the next pass.
func TestMetricsWriteUnderWayIsNotCutShort(t *testing.T) {
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

	// Slower than the pass, well within a write's own limit.
	mock.ExpectExec("insert into `device_metrics`").WillDelayFor(300 * time.Millisecond).WillReturnResult(sqlmock.NewResult(0, 1))
	d.flushMetrics(50*time.Millisecond, 5*time.Second)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	if len(d.metrics) != 1 {
		t.Fatalf("%d counts kept, want only the one never written", len(d.metrics))
	}
	for k, c := range d.metrics {
		if c.requests != 1 || c.in+c.out == 0 {
			t.Errorf("%s: %+v", k.domain, *c)
		}
	}
}
