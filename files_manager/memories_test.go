// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
)

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func days(a, b time.Time) string { return a.Format(time.DateOnly) + ".." + b.Format(time.DateOnly) }

// The days each period covers in an earlier year: the same day, the week
// around it, the month; a 29 February is the 28th in a year without one,
// and the 28th of a year without a 29th shows a leap year's 29th too.
func TestMemoryWindows(t *testing.T) {
	for _, c := range []struct {
		today  string
		period pb.MemoryPeriod
		year   int
		want   string
	}{
		{"2026-10-30", pb.MemoryPeriod_MemoryDay, 2019, "2019-10-30..2019-10-30"},
		{"2026-10-30", pb.MemoryPeriod_MemoryWeek, 2019, "2019-10-27..2019-11-02"},
		{"2026-10-30", pb.MemoryPeriod_MemoryMonth, 2019, "2019-10-01..2019-10-31"},
		{"2026-02-28", pb.MemoryPeriod_MemoryDay, 2024, "2024-02-28..2024-02-29"},
		{"2026-02-28", pb.MemoryPeriod_MemoryDay, 2023, "2023-02-28..2023-02-28"},
		{"2028-02-28", pb.MemoryPeriod_MemoryDay, 2024, "2024-02-28..2024-02-28"},
		{"2028-02-29", pb.MemoryPeriod_MemoryDay, 2025, "2025-02-28..2025-02-28"},
		{"2028-02-29", pb.MemoryPeriod_MemoryDay, 2024, "2024-02-29..2024-02-29"},
		{"2026-02-10", pb.MemoryPeriod_MemoryMonth, 2024, "2024-02-01..2024-02-29"},
		{"2027-01-01", pb.MemoryPeriod_MemoryWeek, 2025, "2024-12-29..2025-01-04"},
	} {
		start, end := memoryWindow(date(c.today), c.period, c.year)
		if got := days(start, end); got != c.want {
			t.Errorf("%s %v in %d: %s, want %s", c.today, c.period, c.year, got, c.want)
		}
	}
}

// A photo belongs to the earlier year whose days it is in - a week around
// New Year to the year of its middle day - and never to this year's.
func TestMemoryYear(t *testing.T) {
	for _, c := range []struct {
		today, taken string
		period       pb.MemoryPeriod
		year         int
		ok           bool
	}{
		{"2026-10-30", "2019-10-30", pb.MemoryPeriod_MemoryDay, 2019, true},
		{"2026-10-30", "2019-10-31", pb.MemoryPeriod_MemoryDay, 0, false},
		{"2026-10-30", "2026-10-30", pb.MemoryPeriod_MemoryDay, 0, false},
		{"2026-10-30", "2019-11-02", pb.MemoryPeriod_MemoryWeek, 2019, true},
		{"2027-01-01", "2024-12-30", pb.MemoryPeriod_MemoryWeek, 2025, true},
		{"2027-01-01", "2026-12-30", pb.MemoryPeriod_MemoryWeek, 0, false}, // this year's week
		{"2026-02-28", "2024-02-29", pb.MemoryPeriod_MemoryDay, 2024, true},
		{"2026-10-30", "1969-10-30", pb.MemoryPeriod_MemoryDay, 0, false},
	} {
		y, ok := memoryYear(date(c.today), c.period, date(c.taken).Add(9*time.Hour))
		if y != c.year || ok != c.ok {
			t.Errorf("%s, taken %s (%v): %d %v, want %d %v", c.today, c.taken, c.period, y, ok, c.year, c.ok)
		}
	}
}

// The days the database is asked for: the windows' and one either side,
// for the time zones; 29 February near the end of February.
func TestMemoryMonthDays(t *testing.T) {
	if got := memoryMonthDays(date("2026-10-30"), pb.MemoryPeriod_MemoryDay); !slices.Equal(got, []string{"10-29", "10-30", "10-31"}) {
		t.Errorf("day: %v", got)
	}
	if got := memoryMonthDays(date("2026-10-30"), pb.MemoryPeriod_MemoryWeek); len(got) != 9 || got[0] != "10-26" || got[8] != "11-03" {
		t.Errorf("week: %v", got)
	}
	if got := memoryMonthDays(date("2026-02-28"), pb.MemoryPeriod_MemoryDay); !slices.Contains(got, "02-29") || !slices.Contains(got, "03-01") {
		t.Errorf("28 February: %v", got)
	}
	// March and a day either side: the 28th, or a leap year's 29th.
	if got := memoryMonthDays(date("2026-03-15"), pb.MemoryPeriod_MemoryMonth); len(got) != 34 || got[0] != "02-28" || got[1] != "02-29" || got[len(got)-1] != "04-01" {
		t.Errorf("month: %d %v", len(got), got)
	}
}

func TestMemoryKeysAndHiding(t *testing.T) {
	for _, c := range []struct {
		period pb.MemoryPeriod
		start  string
		key    string
	}{
		{pb.MemoryPeriod_MemoryDay, "2023-10-30", "day:2023-10-30"},
		{pb.MemoryPeriod_MemoryWeek, "2023-10-27", "week:2023-10-27"},
		{pb.MemoryPeriod_MemoryMonth, "2023-10-01", "month:2023-10"},
	} {
		key := memoryKey(c.period, date(c.start))
		if key != c.key {
			t.Errorf("%v: %s, want %s", c.period, key, c.key)
		}
		p, start, ok := parseMemoryKey(key)
		if !ok || p != c.period || memoryKey(p, start) != key {
			t.Errorf("%s read back as %v %v %v", key, p, start, ok)
		}
	}
	for _, h := range []struct {
		kind, value string
		ok          bool
	}{
		{dao.HideMemoryPhoto, fmt.Sprintf("%064x", 7), true},
		{dao.HideMemoryPhoto, "abc", false},
		{dao.HideMemoryDay, "2023-10-30", true},
		{dao.HideMemoryDay, "30/10/2023", false},
		{dao.HideMemoryMemory, "week:2023-10-27", true},
		{dao.HideMemoryMemory, "year:2023", false},
		{dao.HideMemoryPerson, "6f1c2b9e-2a1d-4c3b-9a8e-1b2c3d4e5f60", true},
		{dao.HideMemoryPerson, "", false},
		{"", "x", false},
	} {
		if got := validHidden(dao.HiddenMemory{Kind: h.kind, Value: h.value}); got != h.ok {
			t.Errorf("%s %q: %v", h.kind, h.value, got)
		}
	}
}

func TestMemoryLocation(t *testing.T) {
	at := time.Date(2026, 7, 1, 22, 30, 0, 0, time.UTC)
	if got := at.In(memoryLocation("Europe/Madrid", 0)).Format(time.DateOnly); got != "2026-07-02" {
		t.Errorf("Madrid in summer: %s", got)
	}
	if got := at.In(memoryLocation("Nowhere/Else", -300)).Format(time.DateOnly); got != "2026-07-01" {
		t.Errorf("an unknown zone takes the offset: %s", got)
	}
	if got := at.In(memoryLocation("", 120)).Format(time.DateOnly); got != "2026-07-02" {
		t.Errorf("an offset: %s", got)
	}
}

// photo is a camera photo taken at local, measured as a good average one.
func photo(n int, local time.Time) memPhoto {
	on := true
	a := float32(5.5)
	return memPhoto{
		hash: fmt.Sprintf("%064x", n), path: fmt.Sprintf("/Phone/IMG_%04d.jpg", n), mime: "image/jpeg",
		created: local, local: local, year: local.Year(),
		quality: &dao.PhotoQuality{Aesthetic: &a, Sharpness: 200, Dark: 0.02, Bright: 0.02, Brightness: 120, Width: 1000, Height: 750, Camera: &on},
	}
}

func dayOpts() memoryOptions {
	return memoryOptions{period: pb.MemoryPeriod_MemoryDay, today: date("2026-10-30"), faces: true, hidden: hiddenSets(nil)}
}

// What is never a memory: what the user hid, screenshots and documents,
// and pictures too blurred, dark, bright or small.
func TestMemoryFilters(t *testing.T) {
	at := time.Date(2020, 10, 30, 12, 0, 0, 0, time.UTC)
	o := dayOpts()
	o.hidden = hiddenSets([]dao.HiddenMemory{{Kind: dao.HideMemoryPhoto, Value: fmt.Sprintf("%064x", 2)}, {Kind: dao.HideMemoryDay, Value: "2020-10-30"}})
	if got := leftOut(photo(2, at.AddDate(0, 0, 1)), o); got != "hidden" {
		t.Errorf("a hidden photo: %q", got)
	}
	if got := leftOut(photo(3, at), o); got != "hidden" {
		t.Errorf("a hidden day: %q", got)
	}
	o = dayOpts()
	o.hidden = hiddenSets([]dao.HiddenMemory{{Kind: dao.HideMemoryPerson, Value: "ex"}})
	withEx := photo(4, at)
	withEx.people = []dao.MemoryFace{{PersonID: "ex", Name: "Ex"}}
	if got := leftOut(withEx, o); got != "hidden" {
		t.Errorf("a hidden person: %q", got)
	}
	o = dayOpts()
	for name, p := range map[string]memPhoto{
		"named a screenshot": func() memPhoto { p := photo(5, at); p.path = "/Phone/Screenshot_20201030.jpg"; return p }(),
		"a Spanish one":      func() memPhoto { p := photo(5, at); p.path = "/Fotos/Captura de pantalla 2020.jpg"; return p }(),
		"a receipt":          func() memPhoto { p := photo(5, at); p.tags = []dao.MemoryTag{{Name: "receipt", Score: 0.9}}; return p }(),
		"text, no camera": func() memPhoto {
			p := photo(5, at)
			p.quality.Camera = new(bool)
			p.tags = []dao.MemoryTag{{Name: "text", Score: 0.9}}
			return p
		}(),
		"a PNG, no camera": func() memPhoto { p := photo(5, at); p.mime = "image/png"; p.quality.Camera = new(bool); return p }(),
		"tall, no camera": func() memPhoto {
			p := photo(5, at)
			p.quality.Camera = new(bool)
			p.quality.Width, p.quality.Height = 462, 1000
			return p
		}(),
		"blurred": func() memPhoto { p := photo(5, at); p.quality.Sharpness = 4; return p }(),
		"dark":    func() memPhoto { p := photo(5, at); p.quality.Dark, p.quality.Brightness = 0.7, 9; return p }(),
		"burnt":   func() memPhoto { p := photo(5, at); p.quality.Bright = 0.65; return p }(),
		"small":   func() memPhoto { p := photo(5, at); p.quality.Width, p.quality.Height = 320, 240; return p }(),
	} {
		if leftOut(p, o) == "" {
			t.Errorf("%s: kept", name)
		}
	}
	kept := map[string]memPhoto{
		"a camera photo":           photo(6, at),
		"a sign in a camera photo": func() memPhoto { p := photo(6, at); p.tags = []dao.MemoryTag{{Name: "text", Score: 0.9}}; return p }(),
		"not measured yet":         func() memPhoto { p := photo(6, at); p.quality = nil; return p }(),
		"a PNG from a camera":      func() memPhoto { p := photo(6, at); p.mime = "image/png"; return p }(),
	}
	for name, p := range kept {
		if why := leftOut(p, o); why != "" {
			t.Errorf("%s: left out as %s", name, why)
		}
	}
}

// noHash: no difference hash is known.
func noHash(string) (uint64, bool) { return 0, false }

// A year's best photos, in the order they were taken: people, a collection
// and a trip beat an ordinary photo; the cover is the best; a burst counts
// once; a quiet year with one ordinary photo has no memory, a notable one
// does; the most recent year comes first.
func TestChooseMemories(t *testing.T) {
	day := func(y, h, m, s int) time.Time { return time.Date(y, 10, 30, h, m, s, 0, time.UTC) }
	var photos []memPhoto
	// 2023: a party (6 photos 20:00-21:00) and a morning walk.
	for i := 0; i < 6; i++ {
		photos = append(photos, photo(100+i, day(2023, 20, i*10, 0)))
	}
	photos[2].people = []dao.MemoryFace{{PersonID: "p1", Name: "Lucía"}, {PersonID: "p2", Name: "Tom"}, {PersonID: "p3"}}
	photos[4].collected = true
	photos = append(photos, photo(110, day(2023, 9, 0, 0)), photo(111, day(2023, 9, 0, 1))) // a burst, 1 s apart
	// 2021: one ordinary photo - not enough for a day.
	photos = append(photos, photo(200, day(2021, 12, 0, 0)))
	// 2020: one photo, posted and liked - a memory on its own.
	posted := photo(300, day(2020, 12, 0, 0))
	posted.posts = dao.PostStats{Posts: 1, Likes: 3}
	photos = append(photos, posted)

	got := chooseFromPhotos(photos, dayOpts(), noHash)
	if len(got) != 2 || got[0].year != 2023 || got[1].year != 2020 {
		t.Fatalf("memories %+v, want 2023 then 2020", got)
	}
	m := got[0]
	if m.key != "day:2023-10-30" || days(m.start, m.end) != "2023-10-30..2023-10-30" || m.photoCount != 8 {
		t.Errorf("2023: %s %s %d photos", m.key, days(m.start, m.end), m.photoCount)
	}
	if photos[m.cover].hash != photos[2].hash {
		t.Errorf("cover %s, want the photo of three people", photos[m.cover].path)
	}
	for n := 1; n < len(m.picks); n++ {
		if photos[m.picks[n]].local.Before(photos[m.picks[n-1]].local) {
			t.Error("picks not in the order they were taken")
		}
	}
	burst := 0
	for _, i := range m.picks {
		if i == 6 || i == 7 {
			burst++
		}
	}
	if burst != 1 {
		t.Errorf("the burst counted %d times", burst)
	}
	if !slices.Equal(m.people, []string{"Lucía", "Tom"}) {
		t.Errorf("people %v", m.people)
	}
	if len(got[1].picks) != 1 {
		t.Errorf("2020: %d picks", len(got[1].picks))
	}

	// Without face recognition, people neither count nor are named.
	o := dayOpts()
	o.faces = false
	got = chooseFromPhotos(photos, o, noHash)
	if len(got[0].people) != 0 {
		t.Errorf("people named with faces off: %v", got[0].people)
	}
	if photos[got[0].cover].hash != photos[4].hash {
		t.Errorf("faces off: cover %s, want the collected one", photos[got[0].cover].path)
	}

	// A hidden memory is left out.
	o = dayOpts()
	o.hidden = hiddenSets([]dao.HiddenMemory{{Kind: dao.HideMemoryMemory, Value: "day:2023-10-30"}})
	if got := chooseFromPhotos(photos, o, noHash); len(got) != 1 || got[0].year != 2020 {
		t.Errorf("2023 hidden: %+v", got)
	}
}

// Shots of one burst a minute apart are told apart by their difference
// hashes: near ones count once, different scenes both.
func TestChooseMemoriesBurstByHash(t *testing.T) {
	at := time.Date(2022, 10, 30, 15, 0, 0, 0, time.UTC)
	photos := []memPhoto{photo(1, at), photo(2, at.Add(40*time.Second)), photo(3, at.Add(80*time.Second))}
	photos[0].quality.Sharpness = 400 // the sharpest of the burst
	hashes := map[string]uint64{photos[0].hash: 0xF0F0F0F0F0F0F0F0, photos[1].hash: 0xF0F0F0F0F0F0F0F1, photos[2].hash: 0x0F0F0F0F0F0F0F0F}
	dhash := func(h string) (uint64, bool) { v, ok := hashes[h]; return v, ok }
	got := chooseFromPhotos(photos, dayOpts(), dhash)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	var picked []string
	for _, i := range got[0].picks {
		picked = append(picked, photos[i].path)
	}
	if !slices.Equal(picked, []string{photos[0].path, photos[2].path}) {
		t.Errorf("picked %v, want the sharp shot and the other scene", picked)
	}
}

// A month's memory spreads over its moments rather than taking the best
// dozen of one afternoon, and names where most of it was taken: a trip
// counts over home.
func TestChooseMemoriesSpreadsOverMoments(t *testing.T) {
	o := memoryOptions{period: pb.MemoryPeriod_MemoryMonth, today: date("2026-10-30"), hidden: hiddenSets(nil), home: homePlaces{city: "Madrid", country: "Spain"}}
	var photos []memPhoto
	n := 0
	// 20 photos of one afternoon at home, a little better looking...
	for i := 0; i < 20; i++ {
		p := photo(n, time.Date(2024, 10, 5, 14, i*3, 0, 0, time.UTC))
		a := float32(6)
		p.quality.Aesthetic = &a
		p.tags = []dao.MemoryTag{{Name: "Madrid", Score: 1}, {Name: "Spain", Score: 1}}
		photos = append(photos, p)
		n++
	}
	// ...and four days of a trip, two photos a day.
	for d := 0; d < 4; d++ {
		for i := 0; i < 2; i++ {
			p := photo(n, time.Date(2024, 10, 20+d, 10+i*4, 0, 0, 0, time.UTC))
			p.tags = []dao.MemoryTag{{Name: "Lisbon", Score: 1}, {Name: "Portugal", Score: 1}}
			photos = append(photos, p)
			n++
		}
	}
	got := chooseFromPhotos(photos, o, noHash)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	m := got[0]
	most, _ := memoryLimits(pb.MemoryPeriod_MemoryMonth)
	trip := 0
	for _, i := range m.picks {
		if i >= 20 {
			trip++
		}
	}
	// Nine moments: the afternoon gives at most three, every day of the
	// trip is there.
	if trip != 8 || len(m.picks)-trip > 3 || len(m.picks) > most {
		t.Errorf("%d photos of the trip among %d", trip, len(m.picks))
	}
	if m.key != "month:2024-10" || m.place != "Lisbon" {
		t.Errorf("key %s place %q", m.key, m.place)
	}
}

// The switch is read once; a database before it is off; turning it on
// starts measuring (nothing to measure without a session here), off keeps
// the measures; while off GetMemories answers ErrMemoriesOff.
func TestMemoriesSwitch(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("select `memories_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"m"}).AddRow(0))
	mg := &Manager{dao: dao.NewWithDB(db)}
	if mg.MemoriesEnabled() || mg.MemoriesEnabled() {
		t.Error("off: want off")
	}
	if _, err := mg.Memories(nil, &pb.GetMemories{Date: "2026-10-30"}); !errors.Is(err, ErrMemoriesOff) {
		t.Errorf("while off: %v", err)
	}
	gen := mg.memories.gen.Load()
	mock.ExpectExec("update `settings` set `memories_enabled` = ?").WithArgs(true).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.SetMemoriesEnabled(nil, true); err != nil || !mg.MemoriesEnabled() {
		t.Errorf("on: %v", err)
	}
	if mg.memories.gen.Load() == gen {
		t.Error("the answers kept weren't dropped")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	db2, mock2, _ := sqlmock.New()
	defer db2.Close()
	mock2.ExpectQuery("select `memories_enabled` from `settings`").WillReturnError(&mysql.MySQLError{Number: 1054, Message: "Unknown column 'memories_enabled'"})
	mg = &Manager{dao: dao.NewWithDB(db2)}
	if mg.MemoriesEnabled() || mg.MemoriesEnabled() {
		t.Error("before the release: want off")
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A bad date or period is refused before anything is read.
func TestMemoriesRefusesABadDate(t *testing.T) {
	mg := &Manager{}
	mg.memories.read, mg.memories.on = true, true
	mg.dao = dao.NewWithDB(nil)
	for _, req := range []*pb.GetMemories{{Date: "30/10/2026"}, {Date: ""}, {Date: "2026-10-30", Period: 9}} {
		if _, err := mg.Memories(nil, req); err == nil || errors.Is(err, ErrMemoriesOff) {
			t.Errorf("%+v: %v", req, err)
		}
	}
}
