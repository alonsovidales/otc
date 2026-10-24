// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"cmp"
	"database/sql"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The admin panel's Growth tab (GET /admin/api/growth): how many devices
// there are and how many are used, how many people open the public site
// and from where (site_visits, visits.go), and how many download the
// Raspberry Pi image, the desktop apps and each device release
// (download_counts, downloads.go). Counts only, nothing that names a
// device, an account or a visitor. Days are UTC dates; the period is the
// last Days days, today included.

const (
	cGrowthTopPages   = 20
	cGrowthTopSources = 30
	cGrowthReleases   = 10
)

// ReleaseSourcePrefix names a device release's source archive in
// download_counts: "src.tar.gz@v136" is release 136's src.tar.gz. Every
// device downloads its release's source to update (scripts/update.sh), so
// its count is how many devices took that release.
const ReleaseSourcePrefix = "src.tar.gz@v"

// Growth is GET /admin/api/growth's answer.
type Growth struct {
	Days      int             `json:"days"`
	Today     string          `json:"today"`
	Devices   GrowthDevices   `json:"devices"`
	Visits    GrowthVisits    `json:"visits"`
	Downloads GrowthDownloads `json:"downloads"`
}

// GrowthDevices: Names is every device name registered (a devices row,
// additional users' addresses included). A device is active when a client
// reached it through the bridge in the last 1, 7 or 30 days:
// devices.last_client_at, which every relayed request stamps (at most once
// a minute, touchLastClient) - its owner's apps or browser away from home,
// or a friend's device. That is the only sign of use the bridge has of a
// device, and it is exact to the minute where device_metrics' hours are
// not: a device used only on its own network, or online with nobody
// reaching it, isn't active, and a released name is gone with its row.
// NewPerWeek is the names registered in each week of the period (Monday to
// Sunday, UTC, the first one whole), by devices.created: set by every
// registration with an account (the setup wizard's claim, the account
// page, a user's address on an account's device). A name registered
// without one - the admin panel, open registration, the users of a device
// from before accounts - has no date and is not in it.
type GrowthDevices struct {
	Names      int         `json:"names"`
	Active1d   int         `json:"active_1d"`
	Active7d   int         `json:"active_7d"`
	Active30d  int         `json:"active_30d"`
	NewPerWeek []WeekCount `json:"new_per_week"`
}

// WeekCount is a week's new names; Week is its Monday.
type WeekCount struct {
	Week  string `json:"week"`
	Names int    `json:"names"`
}

// GrowthVisits: PerDay is every day of the period, oldest first, zero
// for a day without visits; Last7d the last seven of them. Pages and
// Sources are the most visited pages and the sources most visits came
// from over the period, most first. A source is "ref:<name>" for one of
// our own links, a website's name, or "" for none (typed in, a bookmark,
// an app that doesn't say).
type GrowthVisits struct {
	PerDay  []DayCount   `json:"per_day"`
	Total   int64        `json:"total"`
	Last7d  int64        `json:"last_7d"`
	Pages   []NamedCount `json:"pages"`
	Sources []NamedCount `json:"sources"`
}

type DayCount struct {
	Day    string `json:"day"`
	Visits int64  `json:"visits"`
}

type NamedCount struct {
	Name   string `json:"name"`
	Visits int64  `json:"visits"`
}

// GrowthDownloads: Day is the latest poll's, "" before the first one.
// Assets are the image and desktop releases' files the latest poll saw,
// most downloaded first; Releases the source archives of the last
// cGrowthReleases device releases, newest first.
type GrowthDownloads struct {
	Day      string           `json:"day"`
	Assets   []AssetDownloads `json:"assets"`
	Releases []AssetDownloads `json:"releases"`
}

// AssetDownloads is one release file: Total as GitHub counts it now, and
// what it gained in the last 7 days and over the period (gainSince).
// Release is "v136" for a device release's source archive.
type AssetDownloads struct {
	Asset   string `json:"asset"`
	Release string `json:"release,omitempty"`
	Total   int64  `json:"total"`
	Week    int64  `json:"change_7d"`
	Period  int64  `json:"change_period"`
}

// Growth reads the Growth tab's figures for the days days up to now's
// (today included).
func (dao *Dao) Growth(days int, now time.Time) (*Growth, error) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	first := today.AddDate(0, 0, -(days - 1))
	g := &Growth{Days: days, Today: today.Format(time.DateOnly)}
	if err := dao.growthDevices(&g.Devices, now, first, today); err != nil {
		return nil, err
	}
	if err := dao.growthVisits(&g.Visits, first, today); err != nil {
		return nil, err
	}
	if err := dao.growthDownloads(&g.Downloads, days, today); err != nil {
		return nil, err
	}
	return g, nil
}

func (dao *Dao) growthDevices(d *GrowthDevices, now, first, today time.Time) error {
	if err := dao.db.QueryRow(
		"select count(*), coalesce(sum(`last_client_at` >= ?), 0), coalesce(sum(`last_client_at` >= ?), 0), coalesce(sum(`last_client_at` >= ?), 0) from `devices`",
		now.Add(-24*time.Hour), now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour),
	).Scan(&d.Names, &d.Active1d, &d.Active7d, &d.Active30d); err != nil {
		return err
	}

	monday := first.AddDate(0, 0, -((int(first.Weekday()) + 6) % 7))
	rows, err := dao.db.Query(
		"select date(`created`) - interval weekday(`created`) day as `monday`, count(*) from `devices` "+
			"where `created` >= ? group by `monday` order by `monday`", monday)
	if err != nil {
		return err
	}
	defer rows.Close()
	byWeek := map[string]int{}
	for rows.Next() {
		var week time.Time
		var n int
		if err := rows.Scan(&week, &n); err != nil {
			return err
		}
		byWeek[week.Format(time.DateOnly)] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	d.NewPerWeek = []WeekCount{}
	for w := monday; !w.After(today); w = w.AddDate(0, 0, 7) {
		d.NewPerWeek = append(d.NewPerWeek, WeekCount{Week: w.Format(time.DateOnly), Names: byWeek[w.Format(time.DateOnly)]})
	}
	return nil
}

func (dao *Dao) growthVisits(v *GrowthVisits, first, today time.Time) error {
	from := first.Format(time.DateOnly)
	rows, err := dao.db.Query("select `day`, sum(`visits`) from `site_visits` where `day` >= ? group by `day` order by `day`", from)
	if err != nil {
		return err
	}
	defer rows.Close()
	byDay := map[string]int64{}
	for rows.Next() {
		var day time.Time
		var n int64
		if err := rows.Scan(&day, &n); err != nil {
			return err
		}
		byDay[day.Format(time.DateOnly)] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	v.PerDay = []DayCount{}
	weekFrom := today.AddDate(0, 0, -6)
	for d := first; !d.After(today); d = d.AddDate(0, 0, 1) {
		n := byDay[d.Format(time.DateOnly)]
		v.PerDay = append(v.PerDay, DayCount{Day: d.Format(time.DateOnly), Visits: n})
		v.Total += n
		if !d.Before(weekFrom) {
			v.Last7d += n
		}
	}

	if v.Pages, err = dao.topVisits("page", from, cGrowthTopPages); err != nil {
		return err
	}
	v.Sources, err = dao.topVisits("source", from, cGrowthTopSources)
	return err
}

// topVisits is column's values (page or source) with the most visits
// since the day from, most first.
func (dao *Dao) topVisits(column, from string, limit int) ([]NamedCount, error) {
	rows, err := dao.db.Query(
		"select `"+column+"`, sum(`visits`) as `n` from `site_visits` where `day` >= ? "+
			"group by `"+column+"` order by `n` desc, `"+column+"` limit ?", from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NamedCount{}
	for rows.Next() {
		var c NamedCount
		if err := rows.Scan(&c.Name, &c.Visits); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// snapshot is one day's total of a file.
type snapshot struct {
	day time.Time
	n   int64
}

func (dao *Dao) growthDownloads(dl *GrowthDownloads, days int, today time.Time) error {
	dl.Assets, dl.Releases = []AssetDownloads{}, []AssetDownloads{}
	var firstPoll, lastPoll sql.NullTime
	if err := dao.db.QueryRow("select min(`day`), max(`day`) from `download_counts`").Scan(&firstPoll, &lastPoll); err != nil {
		return err
	}
	if !lastPoll.Valid {
		return nil
	}
	dl.Day = lastPoll.Time.Format(time.DateOnly)

	// Each file's last snapshot on or before the period's first day's eve
	// (what it gained is counted from there), then every one after it.
	base := today.AddDate(0, 0, -days)
	series := map[string][]snapshot{}
	if err := dao.scanSnapshots(series,
		"select c.`asset`, c.`day`, c.`downloads` from `download_counts` c join "+
			"(select `asset`, max(`day`) as `day` from `download_counts` where `day` <= ? group by `asset`) b "+
			"on b.`asset` = c.`asset` and b.`day` = c.`day`", base.Format(time.DateOnly)); err != nil {
		return err
	}
	if err := dao.scanSnapshots(series, "select `asset`, `day`, `downloads` from `download_counts` where `day` > ?", base.Format(time.DateOnly)); err != nil {
		return err
	}

	type release struct {
		n int
		a AssetDownloads
	}
	var releases []release
	weekAgo := today.AddDate(0, 0, -7)
	for asset, s := range series {
		slices.SortFunc(s, func(a, b snapshot) int { return a.day.Compare(b.day) })
		last := s[len(s)-1]
		// One the latest poll didn't see is no longer on GitHub.
		if !last.day.Equal(lastPoll.Time) {
			continue
		}
		a := AssetDownloads{Asset: asset, Total: last.n, Week: gainSince(s, weekAgo, firstPoll.Time), Period: gainSince(s, base, firstPoll.Time)}
		if n, ok := releaseNumber(asset); ok {
			a.Release = "v" + strconv.Itoa(n)
			releases = append(releases, release{n, a})
			continue
		}
		dl.Assets = append(dl.Assets, a)
	}
	slices.SortFunc(dl.Assets, func(a, b AssetDownloads) int {
		return cmp.Or(cmp.Compare(b.Total, a.Total), strings.Compare(a.Asset, b.Asset))
	})
	slices.SortFunc(releases, func(a, b release) int { return cmp.Compare(b.n, a.n) })
	for _, r := range releases[:min(len(releases), cGrowthReleases)] {
		dl.Releases = append(dl.Releases, r.a)
	}
	return nil
}

func (dao *Dao) scanSnapshots(series map[string][]snapshot, query string, args ...any) error {
	rows, err := dao.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var asset string
		var s snapshot
		if err := rows.Scan(&asset, &s.day, &s.n); err != nil {
			return err
		}
		series[asset] = append(series[asset], s)
	}
	return rows.Err()
}

// releaseNumber is the release whose source archive asset names
// (ReleaseSourcePrefix).
func releaseNumber(asset string) (int, bool) {
	v, ok := strings.CutPrefix(asset, ReleaseSourcePrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 0
}

// gainSince is how many downloads a file's snapshots s (oldest first)
// gained after the day since: the sum of the rises from its last snapshot
// on or before that day. A total that went down started again from zero -
// the file was uploaded again (the image and the desktop apps are replaced
// in place, and GitHub counts a new upload from 0) - so all of the lower
// total is new. Without a snapshot that old, a file that first shows up
// after the first poll is new and counted from 0; one there from the
// first poll on is counted from that first total, as what came before
// polling began isn't known.
func gainSince(s []snapshot, since, firstPoll time.Time) int64 {
	var start int
	var prev int64
	switch i := sort.Search(len(s), func(i int) bool { return s[i].day.After(since) }); {
	case i > 0:
		start, prev = i, s[i-1].n
	case s[0].day.After(firstPoll):
		start, prev = 0, 0
	default:
		start, prev = 1, s[0].n
	}
	var gain int64
	for _, x := range s[start:] {
		if x.n >= prev {
			gain += x.n - prev
		} else {
			gain += x.n
		}
		prev = x.n
	}
	return gain
}
