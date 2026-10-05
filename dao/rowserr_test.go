// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// A result cut off mid-way (MariaDB restarted) only shows in rows.Err():
// it must come back as an error, not as a shorter list.

var errConnLost = errors.New("connection lost")

func TestGetEventsReportsACutOffPage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	mock.ExpectQuery("from `events`").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "dt", "type", "content"}).
			AddRow("e1", now, "comment", "{}").
			AddRow("e2", now, "comment", "{}").
			RowError(1, errConnLost))
	if events, err := NewWithDB(db).GetEvents(time.Unix(0, 0), 20, ""); err == nil {
		t.Fatalf("a cut-off page was served as complete: %d events", len(events))
	}
}

func TestDelFileVersionsKeepsTheRowsOfACutOffList(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `hash` from `file_versions` where `path` = \\?").
		WithArgs("/a.jpg").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("h1").AddRow("h2").
			RowError(1, errConnLost))
	// No delete may run: sqlmock would answer it with an error of its own.
	if hashes, err := NewWithDB(db).DelFileVersions("/a.jpg"); !errors.Is(err, errConnLost) {
		t.Fatalf("a cut-off list was used: %v, %v", hashes, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
