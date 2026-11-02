// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/go-sql-driver/mysql"
)

// Image Tagging on a device's memory: a device under 3 GB turns it off
// whatever was chosen; a 4 GB one applies its default (off) to a library
// whose user hasn't chosen, a bigger one its default (on); both leave a
// database before release 158's column as it is.
func TestApplyTaggingDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("update `settings` set `image_tagging_enabled` = 0 where `image_tagging_enabled` <> 0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	NewWithDAO(dao.NewWithDB(db)).applyTaggingDefault(MemoryProfile{Low: true, Total: 2 << 30})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("2 GB: %v", err)
	}
	db.Close()

	for _, c := range []struct {
		name string
		p    MemoryProfile
		on   bool
	}{
		{"4 GB", MemoryProfile{Low: true, Total: 4 << 30}, false},
		{"8 GB set to low-memory", MemoryProfile{Low: true, Total: 8 << 30, Source: "config"}, false},
		{"8 GB", MemoryProfile{Total: 8 << 30}, true},
		{"2 GB set to low-memory off", MemoryProfile{Total: 2 << 30, Source: "config"}, true},
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

	for _, p := range []MemoryProfile{{Total: 8 << 30}, {Low: true, Total: 4 << 30}} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectExec("update `settings` set `image_tagging_enabled`").
			WillReturnError(&mysql.MySQLError{Number: 1054, Message: "Unknown column 'image_tagging_chosen'"})
		NewWithDAO(dao.NewWithDB(db)).applyTaggingDefault(p)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	}
}

// What each size of device allows (owner, 2026-11-01; Image Tagging opt-in
// on 4 GB since 2026-11-03, issue #209): 8 GB everything, 4 GB everything
// with Image Tagging heavy (off until chosen), 2 GB neither tagging nor
// other users; a device of unknown size everything, heavy when its profile
// is low-memory.
func TestMemoryTiers(t *testing.T) {
	for _, c := range []struct {
		name                 string
		p                    MemoryProfile
		tagging, heavy, more bool
	}{
		{"8 GB Pi", memoryProfileFor(7900<<20, "", ""), true, false, true},
		{"4 GB Pi", memoryProfileFor(3700<<20, "", ""), true, true, true},
		{"2 GB Pi", memoryProfileFor(1900<<20, "", ""), false, false, false},
		{"unknown", memoryProfileFor(0, "", ""), true, false, true},
		{"unknown set to low-memory", memoryProfileFor(0, "on", ""), true, true, true},
		{"8 GB set to low-memory", memoryProfileFor(7900<<20, "on", ""), true, true, true},
		{"4 GB set to low-memory off", memoryProfileFor(3700<<20, "off", ""), true, false, true},
		{"2 GB set to low-memory off", memoryProfileFor(1900<<20, "off", ""), true, false, false},
		{"4 GB, the primary's low-memory", memoryProfileFor(3700<<20, "", "on"), true, true, true},
	} {
		if got := c.p.TaggingAllowed(); got != c.tagging {
			t.Errorf("%s: tagging %v, want %v", c.name, got, c.tagging)
		}
		if got := c.p.TaggingHeavy(); got != c.heavy {
			t.Errorf("%s: heavy %v, want %v", c.name, got, c.heavy)
		}
		if got := c.p.ExtraUsersAllowed(); got != c.more {
			t.Errorf("%s: other users %v, want %v", c.name, got, c.more)
		}
	}
}

// A device under 3 GB never tags, whatever its setting says; elsewhere -
// a 4 GB one included - the setting decides.
func TestImageTaggingEnabledFollowsTheMemory(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := NewWithDAO(dao.NewWithDB(db))
	mg.profile = MemoryProfile{Low: true, Total: 2 << 30}
	if mg.imageTaggingEnabled() || mg.TaggingAllowed() {
		t.Error("a 2 GB device tags")
	}
	for _, c := range []struct {
		name string
		p    MemoryProfile
		on   bool
	}{
		{"4 GB, on", MemoryProfile{Low: true, Total: 4 << 30}, true},
		{"4 GB, off", MemoryProfile{Low: true, Total: 4 << 30}, false},
		{"8 GB, on", MemoryProfile{Total: 8 << 30}, true},
		{"8 GB, off", MemoryProfile{Total: 8 << 30}, false},
	} {
		mg.profile = c.p
		mock.ExpectQuery("select `image_tagging_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"image_tagging_enabled"}).AddRow(c.on))
		if got := mg.imageTaggingEnabled(); got != c.on || !mg.TaggingAllowed() {
			t.Errorf("%s: tags %v, allowed %v", c.name, got, mg.TaggingAllowed())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
