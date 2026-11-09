// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// noByHandTable is what MySQL answers a statement naming people_by_hand on
// a database whose release 179 script hasn't run.
var noByHandTable = &mysql.MySQLError{Number: 1146, Message: "Table 'otc.people_by_hand' doesn't exist"}

// A read naming people_by_hand runs again with the empty table in its
// place - the same arguments - on a database without it; another missing
// table is no reason to.
func TestByHandReadsGoOnWithoutTheTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectQuery("from `people_by_hand` as `b` join `people`").WithArgs("H").WillReturnError(noByHandTable)
	mock.ExpectQuery("from \\(select `hash`, `person_id`, `created` from `faces` where 0 = 1\\) as `b` join `people`").WithArgs("H").
		WillReturnRows(sqlmock.NewRows([]string{"person_id", "name", "thumbnail"}))
	if people, err := d.PeopleByHandIn("H"); err != nil || len(people) != 0 {
		t.Errorf("without the table: %+v, %v", people, err)
	}

	other := &mysql.MySQLError{Number: 1146, Message: "Table 'otc.people' doesn't exist"}
	mock.ExpectQuery("from `people_by_hand` as `b` join `people`").WithArgs("H").WillReturnError(other)
	if _, err := d.PeopleByHandIn("H"); !errors.Is(err, other) {
		t.Errorf("another table missing: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Info's people added by hand come in the order they were added, each
// with the face People shows for them, as stored.
func TestPeopleByHandIn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `b`.`person_id`, `p`.`name`, coalesce\\(`p`.`cover_image`, .* from `people_by_hand` as `b` join `people` as `p` on `p`.`id` = `b`.`person_id` where `b`.`hash` = \\? order by `b`.`created`, `b`.`person_id`").
		WithArgs("H").WillReturnRows(sqlmock.NewRows([]string{"person_id", "name", "thumbnail"}).
		AddRow("ana", "Ana", []byte("sealed")).AddRow("p2", "", nil))
	people, err := NewWithDB(db).PeopleByHandIn("H")
	if err != nil || len(people) != 2 {
		t.Fatalf("PeopleByHandIn = %+v, %v", people, err)
	}
	if p := people[0]; p.PersonID != "ana" || p.Name != "Ana" || string(p.Thumbnail) != "sealed" {
		t.Errorf("first: %+v", p)
	}
	if p := people[1]; p.PersonID != "p2" || p.Name != "" || p.Thumbnail != nil {
		t.Errorf("second: %+v", p)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// AddToPerson locks the person first: gone, nothing is read or written.
// Hashes that no file holds as a photo or video aren't held; on a database
// without the table it is ErrSchemaBehind.
func TestAddToPersonRefusals(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectBegin()
	mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs("gone").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectRollback()
	if _, _, err := d.AddToPerson("gone", []string{"A"}); !errors.Is(err, ErrNoSuchPerson) {
		t.Errorf("a person who is gone: %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("select distinct `f`.`hash`, .* from `files` as `f` where `f`.`hash` in \\(\\?,\\?\\) and \\(`f`.`mime` like 'image/%' or `f`.`mime` like 'video/%'\\)").
		WithArgs("p1", "A", "B").WillReturnRows(sqlmock.NewRows([]string{"hash", "free"}))
	mock.ExpectRollback()
	if added, held, err := d.AddToPerson("p1", []string{"A", "B", "A"}); err != nil || added != 0 || held {
		t.Errorf("nothing held: %d, %v, %v", added, held, err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("select distinct `f`.`hash`").WithArgs("p1", "A").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "free"}).AddRow("A", true))
	mock.ExpectExec("insert ignore into `people_by_hand` \\(`hash`, `person_id`, `created`\\) values \\(\\?, \\?, \\?\\)").
		WithArgs("A", "p1", sqlmock.AnyArg()).WillReturnError(noByHandTable)
	mock.ExpectRollback()
	if _, _, err := d.AddToPerson("p1", []string{"A"}); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("without the table: %v", err)
	}

	if added, held, err := d.AddToPerson("p1", nil); err != nil || added != 0 || held {
		t.Errorf("no hashes: %d, %v, %v", added, held, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
