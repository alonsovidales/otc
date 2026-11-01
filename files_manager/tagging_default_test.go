// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/go-sql-driver/mysql"
)

// Image Tagging's default follows the memory: off on a low-memory device,
// on elsewhere, applied to a library whose user hasn't chosen; a database
// before release 158's column is left as it is.
func TestApplyTaggingDefault(t *testing.T) {
	for _, c := range []struct {
		name string
		p    MemoryProfile
		on   bool
	}{
		{"4 GB", MemoryProfile{Low: true, Total: 4 << 30}, false},
		{"8 GB", MemoryProfile{Total: 8 << 30}, true},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectExec("update `settings` set `image_tagging_enabled` = \\? where `image_tagging_chosen` = 0").
			WithArgs(c.on, c.on).WillReturnResult(sqlmock.NewResult(0, 1))
		NewWithDAO(dao.NewWithDB(db)).applyTaggingDefault(c.p)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		db.Close()
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("update `settings` set `image_tagging_enabled`").
		WillReturnError(&mysql.MySQLError{Number: 1054, Message: "Unknown column 'image_tagging_chosen'"})
	NewWithDAO(dao.NewWithDB(db)).applyTaggingDefault(MemoryProfile{Low: true})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
