// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Many domains go in chunks, and an error anywhere is an error, never a
// partial answer (the caller closes connections for what is missing).
func TestDeviceOwnersChunksAndFailsWhole(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dao := NewWithDB(db)

	domains := make([]string, cOwnersChunk+1)
	for i := range domains {
		domains[i] = fmt.Sprintf("d%d.otc", i)
	}
	mock.ExpectQuery("select `domain`, `owner_uuid` from `devices` where `domain` in").
		WillReturnRows(sqlmock.NewRows([]string{"domain", "owner_uuid"}).AddRow("d0.otc", "o0"))
	mock.ExpectQuery("select `domain`, `owner_uuid` from `devices` where `domain` in \\(\\?\\)").
		WithArgs(domains[cOwnersChunk]).
		WillReturnRows(sqlmock.NewRows([]string{"domain", "owner_uuid"}).AddRow(domains[cOwnersChunk], "oN"))
	got, err := dao.DeviceOwners(context.Background(), domains)
	if err != nil || len(got) != 2 || got["d0.otc"] != "o0" || got[domains[cOwnersChunk]] != "oN" {
		t.Fatalf("got %v, %v", got, err)
	}

	mock.ExpectQuery("select `domain`, `owner_uuid` from `devices` where `domain` in").
		WillReturnRows(sqlmock.NewRows([]string{"domain", "owner_uuid"}).AddRow("d0.otc", "o0"))
	mock.ExpectQuery("select `domain`, `owner_uuid` from `devices` where `domain` in").
		WillReturnError(errors.New("gone away"))
	if got, err := dao.DeviceOwners(context.Background(), domains); err == nil || got != nil {
		t.Fatalf("a failed chunk answered %v, %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
