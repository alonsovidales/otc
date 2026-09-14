// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// PruneOldLogs is the only thing standing between auth_events (which holds
// remote IP addresses) and device_metrics accumulating forever with no
// retention policy - worth pinning down that it actually issues both
// deletes, with the cutoff it was given.
func TestPruneOldLogsDeletesBothTables(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec("delete from `auth_events` where `dt` < \\?").
		WithArgs(before).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("delete from `device_metrics` where `hour_bucket` < \\?").
		WithArgs(before).
		WillReturnResult(sqlmock.NewResult(0, 5))

	d := NewWithDB(db)
	if err := d.PruneOldLogs(before); err != nil {
		t.Fatalf("PruneOldLogs returned an error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}
