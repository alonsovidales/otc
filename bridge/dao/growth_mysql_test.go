// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// Against a real MySQL with the bridge schema and migration 011
// (OTC_TEST_MYSQL_DSN), in ONLY_FULL_GROUP_BY as MySQL 8 runs by default:
// visits flushed twice add up in their row, a second poll the same day
// replaces the first's totals, and the Growth tab's queries read them back
// with the devices' figures.
func TestGrowthMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // the sql_mode below is the connection's
	if _, err := db.Exec("set session sql_mode = concat_ws(',', nullif(@@sql_mode, ''), 'ONLY_FULL_GROUP_BY')"); err != nil {
		t.Fatal(err)
	}
	d := NewWithDB(db)
	const page, source, asset, release, domain = "/growth-mysql-test", "growth-mysql-test.example", "growth-mysql-test.img.xz", "src.tar.gz@v999999", "growth-mysql-test.example"
	cleanup := func() {
		db.Exec("delete from `site_visits` where `page` = ?", page)
		db.Exec("delete from `download_counts` where `asset` in (?, ?)", asset, release)
		db.Exec("delete from `devices` where `domain` = ?", domain)
	}
	cleanup()
	defer cleanup()
	now := time.Now().UTC()
	today := now.Format(time.DateOnly)

	for range 3 {
		d.recordSiteVisit(today, page, source)
	}
	d.flushVisits(time.Minute, time.Minute)
	d.recordSiteVisit(today, page, source)
	d.recordSiteVisit(today, page, "")
	d.flushVisits(time.Minute, time.Minute)
	if len(d.visits) != 0 {
		t.Fatalf("%d counts not written", len(d.visits))
	}
	var n int
	if err := db.QueryRow("select `visits` from `site_visits` where `day` = ? and `page` = ? and `source` = ?", today, page, source).Scan(&n); err != nil || n != 4 {
		t.Fatalf("visits %d, %v", n, err)
	}

	// The Growth tab's "last snapshot before the period" reads the
	// (asset, day) key, not the whole table.
	var keyed int
	if err := db.QueryRow("select count(*) from information_schema.statistics where table_schema = database() and table_name = 'download_counts' and column_name = 'asset' and seq_in_index = 1").Scan(&keyed); err != nil || keyed == 0 {
		t.Fatalf("download_counts has no (asset, day) key: %d, %v", keyed, err)
	}

	yesterday := now.AddDate(0, 0, -1)
	if err := d.SaveDownloadCounts(context.Background(), yesterday, map[string]int64{asset: 10, release: 3}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveDownloadCounts(context.Background(), now, map[string]int64{asset: 12, release: 4}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveDownloadCounts(context.Background(), now, map[string]int64{asset: 15, release: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into `devices` (`domain`, `owner_uuid`, `secret`, `created`, `last_client_at`) values (?, 'owner', 'secret', ?, ?)", domain, now, now); err != nil {
		t.Fatal(err)
	}

	g, err := d.Growth(7, now)
	if err != nil {
		t.Fatal(err)
	}
	if g.Devices.Names < 1 || g.Devices.Active1d < 1 || len(g.Devices.NewPerWeek) < 1 || g.Devices.NewPerWeek[len(g.Devices.NewPerWeek)-1].Names < 1 {
		t.Errorf("devices: %+v", g.Devices)
	}
	if last := g.Visits.PerDay[len(g.Visits.PerDay)-1]; last.Day != today || last.Visits < 5 {
		t.Errorf("today's visits: %+v", last)
	}
	found := false
	for _, p := range g.Visits.Pages {
		found = found || p == NamedCount{page, 5}
	}
	if !found {
		t.Errorf("pages: %+v", g.Visits.Pages)
	}
	if g.Downloads.Day != today {
		t.Errorf("polled %q", g.Downloads.Day)
	}
	found = false
	for _, a := range g.Downloads.Assets {
		if a.Asset == asset {
			found = true
			// The first snapshot of a file polling saw appear counts from 0
			// only when it came after the first poll ever.
			if a.Total != 15 || a.Week < 5 {
				t.Errorf("%+v", a)
			}
		}
	}
	if !found {
		t.Errorf("assets: %+v", g.Downloads.Assets)
	}
	if len(g.Downloads.Releases) == 0 || g.Downloads.Releases[0] != (AssetDownloads{Asset: release, Release: "v999999", Total: 5, Week: g.Downloads.Releases[0].Week, Period: g.Downloads.Releases[0].Period}) {
		t.Errorf("releases: %+v", g.Downloads.Releases)
	}
}
