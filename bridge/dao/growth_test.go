// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"strconv"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// What a file gained since a day: the rises between its snapshots, a
// total that went down (the file uploaded again) counted whole, and a
// file without a snapshot that old counted from 0 only when it appeared
// after polling began.
func TestGainSince(t *testing.T) {
	first := day("2026-10-01")
	snaps := func(pairs ...any) []snapshot {
		var s []snapshot
		for i := 0; i < len(pairs); i += 2 {
			s = append(s, snapshot{day(pairs[i].(string)), int64(pairs[i+1].(int))})
		}
		return s
	}
	cases := []struct {
		name  string
		s     []snapshot
		since string
		want  int64
	}{
		{"rises from the last snapshot on the day", snaps("2026-10-10", 100, "2026-10-12", 110, "2026-10-15", 130), "2026-10-12", 20},
		{"from the last snapshot before the day", snaps("2026-10-10", 100, "2026-10-14", 115, "2026-10-15", 130), "2026-10-12", 30},
		{"nothing after the day", snaps("2026-10-10", 100), "2026-10-12", 0},
		{"replaced: the lower total is all new", snaps("2026-10-10", 100, "2026-10-12", 120, "2026-10-13", 5, "2026-10-14", 9), "2026-10-11", 20 + 5 + 4},
		{"new since polling began: from 0", snaps("2026-10-13", 8, "2026-10-14", 12), "2026-10-11", 12},
		{"there from the first poll: from its first total", snaps("2026-10-01", 500, "2026-10-14", 512), "2026-09-20", 12},
	}
	for _, c := range cases {
		if got := gainSince(c.s, day(c.since), first); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

// Growth reads every figure and fills in what the rows leave out: every
// day and week of the period, zero where nothing was counted; files the
// latest poll didn't see are left out; the last ten device releases come
// newest first, apart from the other files.
func TestGrowthReadsTheFigures(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC) // a Saturday

	mock.ExpectQuery("select count\\(\\*\\), coalesce\\(sum\\(`last_client_at` >= \\?\\), 0\\).* from `devices`$").
		WithArgs(now.Add(-24*time.Hour), now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"n", "a1", "a7", "a30"}).AddRow(12, 3, 7, 9))
	// The period's first day is Sunday 18 October; its week started on
	// Monday 12.
	mock.ExpectQuery("select date\\(`created`\\) - interval weekday\\(`created`\\) day as `monday`, count\\(\\*\\) from `devices` where `created` >= \\? group by `monday`").
		WithArgs(day("2026-10-12")).
		WillReturnRows(sqlmock.NewRows([]string{"monday", "n"}).AddRow(day("2026-10-19"), 2))
	mock.ExpectQuery("select `day`, sum\\(`visits`\\) from `site_visits` where `day` >= \\? group by `day`").
		WithArgs("2026-10-18").
		WillReturnRows(sqlmock.NewRows([]string{"day", "n"}).AddRow(day("2026-10-18"), 5).AddRow(day("2026-10-24"), 7))
	mock.ExpectQuery("select `page`, sum\\(`visits`\\) as `n` from `site_visits` where `day` >= \\? group by `page` order by `n` desc, `page` limit \\?").
		WithArgs("2026-10-18", cGrowthTopPages).
		WillReturnRows(sqlmock.NewRows([]string{"page", "n"}).AddRow("/", 9).AddRow("/es/", 3))
	mock.ExpectQuery("select `source`, sum\\(`visits`\\) as `n` from `site_visits` where `day` >= \\? group by `source` order by `n` desc, `source` limit \\?").
		WithArgs("2026-10-18", cGrowthTopSources).
		WillReturnRows(sqlmock.NewRows([]string{"source", "n"}).AddRow("", 8).AddRow("ref:reddit", 4))
	mock.ExpectQuery("select min\\(`day`\\), max\\(`day`\\) from `download_counts`").
		WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(day("2026-10-01"), day("2026-10-24")))
	// On or before the period's eve (the 17th): each file's last
	// snapshot; then every one after, in any order. gone.zip is no longer
	// on GitHub; v120 to v136 are device releases.
	baseline := sqlmock.NewRows([]string{"asset", "day", "downloads"}).
		AddRow("off-the-cloud-rpi-lite-arm64.img.xz", day("2026-10-16"), 100).
		AddRow("gone.zip", day("2026-10-10"), 50)
	window := sqlmock.NewRows([]string{"asset", "day", "downloads"}).
		AddRow("off-the-cloud-rpi-lite-arm64.img.xz", day("2026-10-24"), 130).
		AddRow("off-the-cloud-rpi-lite-arm64.img.xz", day("2026-10-18"), 110)
	for v := 120; v <= 136; v++ {
		baseline.AddRow("src.tar.gz@v"+strconv.Itoa(v), day("2026-10-16"), 10)
		window.AddRow("src.tar.gz@v"+strconv.Itoa(v), day("2026-10-24"), 10+v-120)
	}
	mock.ExpectQuery("select c.`asset`, c.`day`, c.`downloads` from `download_counts` c join .* where `day` <= \\? group by `asset`").
		WithArgs("2026-10-17").WillReturnRows(baseline)
	mock.ExpectQuery("select `asset`, `day`, `downloads` from `download_counts` where `day` > \\?").
		WithArgs("2026-10-17").WillReturnRows(window)

	g, err := NewWithDB(db).Growth(7, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	if g.Days != 7 || g.Today != "2026-10-24" {
		t.Errorf("days %d, today %s", g.Days, g.Today)
	}
	if d := g.Devices; d.Names != 12 || d.Active1d != 3 || d.Active7d != 7 || d.Active30d != 9 {
		t.Errorf("devices: %+v", d)
	}
	if w := g.Devices.NewPerWeek; len(w) != 2 || w[0] != (WeekCount{"2026-10-12", 0}) || w[1] != (WeekCount{"2026-10-19", 2}) {
		t.Errorf("weeks: %+v", w)
	}
	v := g.Visits
	if len(v.PerDay) != 7 || v.PerDay[0] != (DayCount{"2026-10-18", 5}) || v.PerDay[1] != (DayCount{"2026-10-19", 0}) || v.PerDay[6] != (DayCount{"2026-10-24", 7}) {
		t.Errorf("per day: %+v", v.PerDay)
	}
	if v.Total != 12 || v.Last7d != 12 {
		t.Errorf("total %d, last 7 days %d", v.Total, v.Last7d)
	}
	if len(v.Pages) != 2 || v.Pages[0] != (NamedCount{"/", 9}) || len(v.Sources) != 2 || v.Sources[1] != (NamedCount{"ref:reddit", 4}) {
		t.Errorf("pages %+v, sources %+v", v.Pages, v.Sources)
	}

	dl := g.Downloads
	if dl.Day != "2026-10-24" {
		t.Errorf("polled %q", dl.Day)
	}
	if len(dl.Assets) != 1 || dl.Assets[0] != (AssetDownloads{Asset: "off-the-cloud-rpi-lite-arm64.img.xz", Total: 130, Week: 30, Period: 30}) {
		t.Errorf("assets: %+v", dl.Assets)
	}
	if len(dl.Releases) != cGrowthReleases || dl.Releases[0].Release != "v136" || dl.Releases[9].Release != "v127" {
		t.Fatalf("releases: %+v", dl.Releases)
	}
	if r := dl.Releases[0]; r.Asset != "src.tar.gz@v136" || r.Total != 26 || r.Week != 16 || r.Period != 16 {
		t.Errorf("v136: %+v", r)
	}
}

// Before the first poll there is nothing to show, and no error.
func TestGrowthDownloadsBeforeTheFirstPoll(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select min\\(`day`\\), max\\(`day`\\) from `download_counts`").
		WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(nil, nil))
	var dl GrowthDownloads
	if err := NewWithDB(db).growthDownloads(&dl, 30, day("2026-10-24")); err != nil {
		t.Fatal(err)
	}
	if dl.Day != "" || dl.Assets == nil || len(dl.Assets) != 0 || dl.Releases == nil || len(dl.Releases) != 0 {
		t.Errorf("%+v", dl)
	}
}
