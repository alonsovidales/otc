// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/go-sql-driver/mysql"
)

// Image Tagging on a device's memory: a low-memory device turns it off
// whatever was chosen; a bigger one applies its default (on) to a library
// whose user hasn't chosen, and leaves a database before release 158's
// column as it is.
func TestApplyTaggingDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("update `settings` set `image_tagging_enabled` = 0 where `image_tagging_enabled` <> 0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	NewWithDAO(dao.NewWithDB(db)).applyTaggingDefault(MemoryProfile{Low: true, Total: 4 << 30})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("4 GB: %v", err)
	}
	db.Close()

	db, mock, err = sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("update `settings` set `image_tagging_enabled` = \\? where `image_tagging_chosen` = 0").
		WithArgs(true, true).WillReturnResult(sqlmock.NewResult(0, 1))
	NewWithDAO(dao.NewWithDB(db)).applyTaggingDefault(MemoryProfile{Total: 8 << 30})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("8 GB: %v", err)
	}
	db.Close()

	db, mock, err = sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("update `settings` set `image_tagging_enabled`").
		WillReturnError(&mysql.MySQLError{Number: 1054, Message: "Unknown column 'image_tagging_chosen'"})
	NewWithDAO(dao.NewWithDB(db)).applyTaggingDefault(MemoryProfile{Total: 8 << 30})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// What each size of device allows (owner, 2026-11-01): 8 GB everything,
// 4 GB no Image Tagging, 2 GB neither tagging nor other users; a device of
// unknown size everything but what its profile says.
func TestMemoryTiers(t *testing.T) {
	for _, c := range []struct {
		name          string
		p             MemoryProfile
		tagging, more bool
	}{
		{"8 GB Pi", memoryProfileFor(7900<<20, "", ""), true, true},
		{"4 GB Pi", memoryProfileFor(3700<<20, "", ""), false, true},
		{"2 GB Pi", memoryProfileFor(1900<<20, "", ""), false, false},
		{"unknown", memoryProfileFor(0, "", ""), true, true},
		{"8 GB set to low-memory", memoryProfileFor(7900<<20, "on", ""), false, true},
	} {
		if got := c.p.TaggingAllowed(); got != c.tagging {
			t.Errorf("%s: tagging %v, want %v", c.name, got, c.tagging)
		}
		if got := c.p.ExtraUsersAllowed(); got != c.more {
			t.Errorf("%s: other users %v, want %v", c.name, got, c.more)
		}
	}
}

// A low-memory device never tags, whatever its setting says; elsewhere the
// setting decides.
func TestImageTaggingEnabledFollowsTheMemory(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := NewWithDAO(dao.NewWithDB(db))
	mg.profile = MemoryProfile{Low: true, Total: 4 << 30}
	if mg.imageTaggingEnabled() || mg.TaggingAllowed() {
		t.Error("a low-memory device tags")
	}
	mg.profile = MemoryProfile{Total: 8 << 30}
	mock.ExpectQuery("select `image_tagging_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"image_tagging_enabled"}).AddRow(true))
	if !mg.imageTaggingEnabled() || !mg.TaggingAllowed() {
		t.Error("an 8 GB device with tagging on doesn't tag")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
