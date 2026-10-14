// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #194: a jump's cutoff goes to the query at the second below it, so
// MySQL can't round it into the next second (MariaDB truncates the
// fraction; the rows are whole seconds either way).
func TestSearchMediaBeforeSendsWholeSeconds(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, before := range []time.Time{
		time.Date(2022, 6, 30, 23, 59, 59, 999_999_999, time.UTC), // the web's end of a month, and finer
		time.Date(2022, 6, 30, 23, 59, 59, 500_000_000, time.UTC),
		time.Date(2022, 6, 30, 23, 59, 59, 0, time.UTC),
	} {
		mock.ExpectQuery("`f`\\.`created` <= \\?").
			WithArgs(time.Date(2022, 6, 30, 23, 59, 59, 0, time.UTC)).
			WillReturnRows(sqlmock.NewRows(searchCols))
		if _, err := NewWithDB(db).SearchMedia("", nil, nil, "", true, &before, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

var searchCols = []string{"hash", "mime", "created", "modified", "path", "size"}

func describeOrder(files []*pb.File) string {
	var out []string
	for _, f := range files {
		out = append(out, f.Hash+"="+f.Path+"@"+f.Created.AsTime().Format("2006-01-02T15:04:05"))
	}
	return strings.Join(out, " ")
}

// The newer half asks the same query as a plain search - no date in it -
// and picks the photos whose OLDEST copy is after the instant: a copy
// synced back after it doesn't bring a photo the jump's half shows, and a
// photo at the instant's very second belongs to that half. Oldest first.
func TestSearchMediaNewerThanPicksByTheOldestCopy(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	end := time.Date(2022, 6, 30, 23, 59, 59, 0, time.UTC)
	rows := func() *sqlmock.Rows {
		r := sqlmock.NewRows(searchCols)
		// As the query orders them: newest first, by path backwards.
		for _, row := range []struct {
			hash, path string
			at         time.Time
		}{
			{"A", "/Download/a.jpg", end.AddDate(4, 0, 0)}, // A, synced back
			{"B", "/Phone/b.jpg", end.AddDate(1, 0, 0)},
			{"S", "/Phone/s.jpg", end.Add(time.Second)}, // S's newer copy
			{"N", "/Phone/n.jpg", end.Add(time.Second)}, // July's first second
			{"M", "/Phone/m.jpg", end},                  // June's last second
			{"S", "/Mac/s.jpg", end},                    // S's oldest: June
			{"A", "/Phone/a.jpg", end.AddDate(-3, 0, 0)},
		} {
			r.AddRow(row.hash, "image/jpeg", row.at, row.at, row.path, 1)
		}
		return r
	}
	d := NewWithDB(db)
	for _, c := range []struct {
		what  string
		after time.Time
		want  string
	}{
		{"the end of June", end, "N=/Phone/n.jpg@2022-07-01T00:00:00 B=/Phone/b.jpg@2023-06-30T23:59:59"},
		{"the end of June, to the nanosecond", end.Add(999_999_999), "N=/Phone/n.jpg@2022-07-01T00:00:00 B=/Phone/b.jpg@2023-06-30T23:59:59"},
		{"a second before", end.Add(-time.Second), "S=/Mac/s.jpg@2022-06-30T23:59:59 M=/Phone/m.jpg@2022-06-30T23:59:59 N=/Phone/n.jpg@2022-07-01T00:00:00 B=/Phone/b.jpg@2023-06-30T23:59:59"},
		{"before everything", end.AddDate(-10, 0, 0), "A=/Phone/a.jpg@2019-06-30T23:59:59 S=/Mac/s.jpg@2022-06-30T23:59:59 M=/Phone/m.jpg@2022-06-30T23:59:59 N=/Phone/n.jpg@2022-07-01T00:00:00 B=/Phone/b.jpg@2023-06-30T23:59:59"},
		{"after everything", end.AddDate(10, 0, 0), ""},
	} {
		mock.ExpectQuery("^select `f`.`hash`, `f`.`mime`, `f`.`created`, `f`.`modified`, `f`.`path`, `f`.`size` from `files` as `f` where `f`.`mime` like 'image%' order by `f`.`created` desc, `f`.`path` desc$").
			WithoutArgs().WillReturnRows(rows())
		files, err := d.SearchMediaNewerThan("", nil, nil, "", true, c.after, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := describeOrder(files); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.what, got, c.want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A tag search's photos come by score; their newer half is by date all
// the same, then by path.
func TestNewerThanOldestFirstSortsATagSearchByDate(t *testing.T) {
	at := func(day int, path string) *pb.File {
		return &pb.File{Hash: path, Path: path, Created: timestamppb.New(time.Date(2024, 1, day, 12, 0, 0, 0, time.UTC))}
	}
	files := []*pb.File{at(5, "/b"), at(9, "/c"), at(1, "/old"), at(5, "/a"), at(7, "/d")}
	got := describeOrder(newerThanOldestFirst(files, time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), true))
	want := "/a=/a@2024-01-05T12:00:00 /b=/b@2024-01-05T12:00:00 /d=/d@2024-01-07T12:00:00 /c=/c@2024-01-09T12:00:00"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
