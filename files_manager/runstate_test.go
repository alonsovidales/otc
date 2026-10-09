// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
)

// fakeKernel is a run's view of the kernel, in files: a boot id, the OOM
// kill counters (system and the service's cgroup) and the marker's place.
type fakeKernel struct {
	dir string
	env runEnv
	now time.Time
}

func newFakeKernel(t *testing.T) *fakeKernel {
	t.Helper()
	dir := t.TempDir()
	k := &fakeKernel{dir: dir, now: time.Date(2026, 10, 8, 19, 34, 25, 0, time.Local)}
	k.env = runEnv{
		statePath:  filepath.Join(dir, "state", cRunStateFile),
		bootIDPath: filepath.Join(dir, "boot_id"),
		vmstatPath: filepath.Join(dir, "vmstat"),
		cgroupPath: filepath.Join(dir, "cgroup"),
		cgroupRoot: filepath.Join(dir, "sys"),
		now:        func() time.Time { return k.now },
	}
	os.MkdirAll(filepath.Join(dir, "state"), 0o700)
	os.MkdirAll(filepath.Join(dir, "sys", "system.slice", "otc.service"), 0o700)
	k.write("cgroup", "0::/system.slice/otc.service\n")
	k.boot("6b1d9d2c-boot-one")
	k.oomKills(3)
	k.cgroupOOMKills(0)
	return k
}

func (k *fakeKernel) write(name, content string) {
	os.WriteFile(filepath.Join(k.dir, name), []byte(content), 0o600)
}

func (k *fakeKernel) boot(id string) { k.write("boot_id", id+"\n") }

func (k *fakeKernel) oomKills(n int) {
	k.write("vmstat", "nr_free_pages 12345\noom_kill "+strconv.Itoa(n)+"\nnr_zone_active_anon 7\n")
}

func (k *fakeKernel) cgroupOOMKills(n int) {
	k.write(filepath.Join("sys", "system.slice", "otc.service", "memory.events"), "low 0\nhigh 0\nmax 0\noom 0\noom_kill "+strconv.Itoa(n)+"\noom_group_kill 0\n")
}

func (k *fakeKernel) start(t *testing.T) (*runState, previousRun) {
	t.Helper()
	rs, prev, err := startRun(k.env)
	if err != nil {
		t.Fatal(err)
	}
	return rs, prev
}

func (k *fakeKernel) marker(t *testing.T) runRecord {
	t.Helper()
	raw, err := os.ReadFile(k.env.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var rec runRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("marker %q: %v", raw, err)
	}
	return rec
}

// A start writes the marker with the boot and the counters; a clean stop
// removes it, and the start after that knows nothing went wrong.
func TestRunMarkerCleanStop(t *testing.T) {
	k := newFakeKernel(t)
	rs, prev := k.start(t)
	if prev.unclean {
		t.Fatal("the first start found a death")
	}
	rec := k.marker(t)
	if rec.Boot != "6b1d9d2c-boot-one" || rec.OOMKills != 3 || rec.CgroupOOMKills != 0 || !rec.Started.Equal(k.now) || rec.Deaths != 0 {
		t.Errorf("marker %+v", rec)
	}
	rs.stop()
	if _, err := os.Stat(k.env.statePath); !os.IsNotExist(err) {
		t.Fatal("a clean stop left the marker")
	}
	_, prev = k.start(t)
	if prev.unclean {
		t.Error("the start after a clean stop found a death")
	}
}

// The kernel's OOM kill counter rose while the run that left its marker
// ran: it ran out of memory.
func TestRunMarkerOOMKill(t *testing.T) {
	k := newFakeKernel(t)
	k.start(t)
	k.oomKills(4)
	k.now = k.now.Add(65 * time.Second)
	_, prev := k.start(t)
	if !prev.unclean || !prev.sameBoot || !prev.oom || prev.deaths != 1 || prev.oomDeaths != 1 || !prev.since.Equal(k.now) {
		t.Fatalf("previous run %+v", prev)
	}
	rec := k.marker(t)
	if rec.Deaths != 1 || rec.OOMDeaths != 1 || rec.OOMKills != 4 {
		t.Errorf("marker %+v", rec)
	}
}

// The service cgroup's own count says so too (a supervised instance
// killed while the primary lives on).
func TestRunMarkerCgroupOOMKill(t *testing.T) {
	k := newFakeKernel(t)
	k.start(t)
	k.cgroupOOMKills(1)
	_, prev := k.start(t)
	if !prev.oom {
		t.Fatalf("previous run %+v", prev)
	}
}

// A death the counters don't explain: not out of memory, still a death.
func TestRunMarkerDeathWithoutOOM(t *testing.T) {
	k := newFakeKernel(t)
	k.start(t)
	_, prev := k.start(t)
	if !prev.unclean || !prev.sameBoot || prev.oom || prev.deaths != 1 || prev.oomDeaths != 0 {
		t.Fatalf("previous run %+v", prev)
	}
	// A cgroup recreated by the restart counts from 0 again: no evidence.
	k.cgroupOOMKills(0)
	k.write(filepath.Join("sys", "system.slice", "otc.service", "memory.events"), "oom_kill 0\n")
	_, prev = k.start(t)
	if prev.oom {
		t.Error("a counter that went down was taken for an OOM kill")
	}
}

// Deaths in a row are counted from the first; the Alert row is kept; a
// run that lived longer than cCrashSeries starts a new count.
func TestRunMarkerCountsDeathsInARow(t *testing.T) {
	k := newFakeKernel(t)
	k.start(t)
	first := k.now.Add(70 * time.Second)
	for i := 1; i <= 12; i++ {
		k.oomKills(3 + i)
		k.now = k.now.Add(70 * time.Second)
		rs, prev := k.start(t)
		if prev.deaths != i || prev.oomDeaths != i || !prev.since.Equal(first) {
			t.Fatalf("death %d: %+v", i, prev)
		}
		if i == 1 {
			rs.setAlert("alert-1")
		}
	}
	if rec := k.marker(t); rec.Alert != "alert-1" || rec.Deaths != 12 {
		t.Errorf("marker %+v", rec)
	}

	k.now = k.now.Add(2 * time.Hour)
	k.oomKills(16)
	_, prev := k.start(t)
	if prev.deaths != 1 || prev.oomDeaths != 1 || !prev.since.Equal(k.now) {
		t.Fatalf("after a long run: %+v", prev)
	}
	if rec := k.marker(t); rec.Alert != "" {
		t.Errorf("a new series kept the old Alert: %+v", rec)
	}
}

// A marker from another boot: the device itself went down. Nothing is
// counted - the counters started again.
func TestRunMarkerOtherBoot(t *testing.T) {
	k := newFakeKernel(t)
	k.start(t)
	k.boot("another-boot")
	k.oomKills(0)
	_, prev := k.start(t)
	if !prev.unclean || prev.sameBoot || prev.oom || prev.deaths != 0 {
		t.Fatalf("previous run %+v", prev)
	}
	if rec := k.marker(t); rec.Deaths != 0 || rec.Boot != "another-boot" {
		t.Errorf("marker %+v", rec)
	}
}

// A marker cut short as it was written: a death, nothing else known.
func TestRunMarkerCorrupt(t *testing.T) {
	k := newFakeKernel(t)
	os.WriteFile(k.env.statePath, []byte(`{"boot":"6b1d9d2c-bo`), 0o600)
	_, prev := k.start(t)
	if !prev.unclean || prev.sameBoot || prev.oom {
		t.Fatalf("previous run %+v", prev)
	}
}

// Content being processed at two deaths is set aside; at one it has a
// strike, carried through the series.
func TestRunMarkerStrikes(t *testing.T) {
	k := newFakeKernel(t)
	h1, h2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	rs, _ := k.start(t)
	rs.track(h1, true)
	rs.track(h2, true)
	rs.track(h2, false)

	k.now = k.now.Add(time.Minute)
	rs, prev := k.start(t)
	if len(prev.setAside) != 0 {
		t.Fatalf("set aside %v at the first death", prev.setAside)
	}
	if rec := k.marker(t); rec.Strikes[h1] != 1 || rec.Strikes[h2] != 0 || len(rec.InFlight) != 0 {
		t.Fatalf("marker %+v", rec)
	}
	rs.track(h2, true)
	rs.track(h1, true)

	k.now = k.now.Add(time.Minute)
	_, prev = k.start(t)
	if len(prev.setAside) != 1 || prev.setAside[0] != h1 {
		t.Fatalf("set aside %v, want %s", prev.setAside, h1)
	}
	if rec := k.marker(t); rec.Strikes[h1] != 0 || rec.Strikes[h2] != 1 {
		t.Errorf("marker %+v", rec)
	}
}

// The marker is rewritten in place - never truncated, still JSON.
func TestRunMarkerRewrittenInPlace(t *testing.T) {
	k := newFakeKernel(t)
	rs, _ := k.start(t)
	h := strings.Repeat("c", 64)
	rs.track(h, true)
	fi1, _ := os.Stat(k.env.statePath)
	rs.track(h, false)
	fi2, _ := os.Stat(k.env.statePath)
	if fi2.Size() != fi1.Size() {
		t.Errorf("the marker went from %d to %d bytes", fi1.Size(), fi2.Size())
	}
	if rec := k.marker(t); len(rec.InFlight) != 0 {
		t.Errorf("marker %+v", rec)
	}
}

func TestProcessingPause(t *testing.T) {
	for deaths, want := range map[int]time.Duration{0: 0, 1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 5: 16 * time.Minute, 6: 30 * time.Minute, 40: 30 * time.Minute} {
		if got := processingPause(deaths); got != want {
			t.Errorf("processingPause(%d) = %v, want %v", deaths, got, want)
		}
	}
}

func TestMemoryAlertWording(t *testing.T) {
	now := time.Date(2026, 10, 8, 19, 51, 46, 0, time.Local)
	since := time.Date(2026, 10, 8, 19, 34, 28, 0, time.Local)
	if got := memoryAlertTitle(previousRun{oomDeaths: 1, since: since}, now); got != "Your device ran out of memory and restarted" {
		t.Errorf("once: %q", got)
	}
	if got := memoryAlertTitle(previousRun{oomDeaths: 12, since: since}, now); got != "Your device ran out of memory and restarted 12 times since 19:34" {
		t.Errorf("12 times: %q", got)
	}
	if got := memoryAlertTitle(previousRun{oomDeaths: 3, since: since}, now.AddDate(0, 0, 1)); got != "Your device ran out of memory and restarted 3 times since 8 Oct 19:34" {
		t.Errorf("since yesterday: %q", got)
	}
	hint := memoryAlertHint(memoryProfileFor(pi4GB, "", ""))
	if !strings.HasPrefix(hint, "This device has 4 GB of memory, less than the 8 GB recommended") || !strings.Contains(hint, "Settings > Logs") {
		t.Errorf("4 GB hint: %q", hint)
	}
	if hint := memoryAlertHint(memoryProfileFor(pi8GB, "", "")); !strings.HasPrefix(hint, "This device has 8 GB of memory.") {
		t.Errorf("8 GB hint: %q", hint)
	}
	if hint := memoryAlertHint(memoryProfileFor(pi8GB, "on", "")); !strings.HasPrefix(hint, "This device has 8 GB of memory: it processes one photo at a time") {
		t.Errorf("8 GB in low-memory mode: %q", hint)
	}
}

func managerWithMock(t *testing.T) (*Manager, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mg := &Manager{dao: dao.NewWithDB(db)}
	t.Cleanup(func() {
		db.Close()
		if mg.run != nil { // its watcher, without touching the marker
			mg.run.mu.Lock()
			mg.run.haltLocked()
			mg.run.mu.Unlock()
		}
	})
	return mg, mock
}

// The deaths of a series are one Alert: raised at the first, its title
// updated with the count at each one after - never a push.
func TestCheckPreviousRunRaisesOneCountingAlert(t *testing.T) {
	k := newFakeKernel(t)
	mg, mock := managerWithMock(t)
	p := memoryProfileFor(pi4GB, "", "")

	// A clean start on a low-memory device: one job at a time, tracked,
	// no pause.
	g := mg.checkPreviousRun(k.env, p)
	if g == nil || !g.limited.Load() || !g.tracking.Load() || !g.resumeAt.IsZero() {
		t.Fatalf("low-memory guard %+v", g)
	}

	k.oomKills(4)
	k.now = k.now.Add(70 * time.Second)
	mock.ExpectExec("insert into `notifications`").
		WithArgs(sqlmock.AnyArg(), "(standalone)", "Your device ran out of memory and restarted", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	g = mg.checkPreviousRun(k.env, p)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if g == nil || !g.limited.Load() || !g.resumeAt.Equal(k.now.Add(time.Minute)) {
		t.Fatalf("guard after a death %+v", g)
	}
	id := k.marker(t).Alert
	if id == "" {
		t.Fatal("the Alert's id isn't in the marker")
	}

	k.oomKills(5)
	k.now = k.now.Add(70 * time.Second)
	mock.ExpectExec(regexp.QuoteMeta("update `notifications` set `title` = ?")).
		WithArgs("Your device ran out of memory and restarted 2 times since 19:35", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), id).
		WillReturnResult(sqlmock.NewResult(0, 1))
	g = mg.checkPreviousRun(k.env, p)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if !g.resumeAt.Equal(k.now.Add(2 * time.Minute)) {
		t.Errorf("second pause until %v, want 2 minutes", g.resumeAt)
	}
	if k.marker(t).Alert != id {
		t.Error("the Alert's id changed")
	}
	mg.Stopped()
	if _, err := os.Stat(k.env.statePath); !os.IsNotExist(err) {
		t.Error("Stopped left the marker")
	}
}

// An 8 GB device: a clean start changes nothing (no guard); a death that
// isn't out of memory, with nothing in flight, is only logged, and the
// run after it goes one file at a time, recorded, with no pause.
func TestCheckPreviousRunNormalProfile(t *testing.T) {
	k := newFakeKernel(t)
	mg, mock := managerWithMock(t)
	p := memoryProfileFor(pi8GB, "", "")
	if g := mg.checkPreviousRun(k.env, p); g != nil {
		t.Fatalf("a clean start on 8 GB got a guard %+v", g)
	}
	k.now = k.now.Add(time.Minute)
	g := mg.checkPreviousRun(k.env, p)
	if g == nil || !g.limited.Load() || !g.tracking.Load() || g.resumeAt.After(k.now) {
		t.Fatalf("guard after a death %+v", g)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if k.marker(t).Alert != "" {
		t.Error("an Alert for a death that wasn't out of memory")
	}
	// After a power cut: no pause, nothing counted.
	k.boot("another-boot")
	if g := mg.checkPreviousRun(k.env, p); g != nil {
		t.Errorf("guard after a power cut %+v", g)
	}
}

// Content being processed at two deaths comes off the pending analyses,
// gets the backfill's marker when it has no thumbnail, and an Alert.
func TestCheckPreviousRunSetsAside(t *testing.T) {
	storage, _ := galleryTestEnv(t)
	k := newFakeKernel(t)
	mg, mock := managerWithMock(t)
	p := memoryProfileFor(pi4GB, "", "")
	h := strings.Repeat("d", 64)
	os.Remove(filepath.Join(storage, h+cNoThumbnailSuffix))
	t.Cleanup(func() { os.Remove(filepath.Join(storage, h+cNoThumbnailSuffix)) })

	mg.checkPreviousRun(k.env, p)
	mg.run.track(h, true)
	k.now = k.now.Add(time.Minute)
	mg.checkPreviousRun(k.env, p)
	mg.run.track(h, true)
	k.now = k.now.Add(time.Minute)

	// An Alert of its own: inserted standalone, never through the
	// grouping that would have made it a line of another row.
	mock.ExpectExec("delete from `pending_analysis`").WithArgs(h).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files`").WithArgs(h).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow(h, "image/heic", time.Now(), time.Now(), "/Photos/IMG_0001.HEIC", 3_000_000))
	mock.ExpectExec("insert into `notifications`").
		WithArgs(sqlmock.AnyArg(), "(standalone)", "IMG_0001.HEIC was set aside", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mg.checkPreviousRun(k.env, p)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(storage, h+cNoThumbnailSuffix)); err != nil || string(raw) != cDecoders {
		t.Errorf("backfill marker %q, %v", raw, err)
	}
	if !mg.isSetAside(h) {
		t.Error("not on the set-aside list")
	}
	if got := mg.fixThumbnails(nil, h, 1000); got != thumbSkipped {
		t.Errorf("a thumbnail fix of set-aside content: %v", got)
	}

	// The next update gives it back: pending again, the marker gone.
	prevID := binaryID
	binaryID = func() string { return "a newer binary" }
	defer func() { binaryID = prevID }()
	mock.ExpectExec("insert ignore into `pending_analysis`").WithArgs(h, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mg.retrySetAside()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if mg.isSetAside(h) {
		t.Error("still set aside after an update")
	}
	if _, err := os.Stat(filepath.Join(storage, h+cNoThumbnailSuffix)); !os.IsNotExist(err) {
		t.Error("the backfill marker stayed")
	}
	mg.Stopped()
}

// Set aside under this binary, recently: kept; long ago: given back.
func TestRetrySetAsideKeepsRecentOnes(t *testing.T) {
	galleryTestEnv(t)
	mg, mock := managerWithMock(t)
	prevID := binaryID
	binaryID = func() string { return "this binary" }
	defer func() { binaryID = prevID }()
	recent, old := strings.Repeat("1", 64), strings.Repeat("2", 64)
	l, path := mg.setAsideList()
	l.mu.Lock()
	l.loadLocked(path)
	l.entries[recent] = setAsideEntry{At: time.Now().Add(-time.Hour), Binary: "this binary"}
	l.entries[old] = setAsideEntry{At: time.Now().Add(-cSetAsideRetry - time.Hour), Binary: "this binary"}
	l.saveLocked(path)
	l.mu.Unlock()
	t.Cleanup(func() {
		l.mu.Lock()
		l.entries = map[string]setAsideEntry{}
		l.saveLocked(path)
		l.mu.Unlock()
	})
	mock.ExpectExec("insert ignore into `pending_analysis`").WithArgs(old, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mg.retrySetAside()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if !mg.isSetAside(recent) || mg.isSetAside(old) {
		t.Errorf("recent set aside %v, old %v", mg.isSetAside(recent), mg.isSetAside(old))
	}
}

// Finding 1: a crash loop that has nothing to do with processing - a
// request's panic every 5 minutes, nothing in flight, memory to spare -
// never pauses processing, so new uploads keep getting thumbnails and the
// analysis keeps up.
func TestNonProcessingCrashLoopNeverPauses(t *testing.T) {
	k := newFakeKernel(t)
	mg, _ := managerWithMock(t)
	p := memoryProfileFor(pi8GB, "", "")
	mg.checkPreviousRun(k.env, p)
	for death := 1; death <= 8; death++ {
		k.now = k.now.Add(5 * time.Minute)
		g := mg.checkPreviousRun(k.env, p)
		if g == nil || g.resumeAt.After(k.now) {
			t.Fatalf("death %d: paused until %v", death, g.resumeAt.Sub(k.now))
		}
	}
	mg.Stopped()
}

// Deaths during the dead run's own pause don't lengthen the next one.
func TestDeathsDuringThePauseDontGrowIt(t *testing.T) {
	k := newFakeKernel(t)
	mg, mock := managerWithMock(t)
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 4; i++ {
		mock.ExpectExec("insert into `notifications`").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("update `notifications`").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	p := memoryProfileFor(pi4GB, "", "")
	mg.checkPreviousRun(k.env, p)
	k.oomKills(4)
	k.now = k.now.Add(10 * time.Minute) // after a while: out of memory
	g := mg.checkPreviousRun(k.env, p)
	if d := g.resumeAt.Sub(k.now); d != time.Minute {
		t.Fatalf("first pause %v", d)
	}
	k.oomKills(5)
	k.now = k.now.Add(20 * time.Second) // within its 1-minute pause
	g = mg.checkPreviousRun(k.env, p)
	if d := g.resumeAt.Sub(k.now); d != time.Minute {
		t.Errorf("pause after a death during the pause %v, want 1m again", d)
	}
	k.oomKills(6)
	k.now = k.now.Add(5 * time.Minute) // after it: longer
	g = mg.checkPreviousRun(k.env, p)
	if d := g.resumeAt.Sub(k.now); d != 2*time.Minute {
		t.Errorf("pause after a death past the pause %v, want 2m", d)
	}
	mg.Stopped()
}

// Finding 2: a death while a model loads isn't a strike on the file in
// flight - photos used to be set aside one by one for RAM++'s peak.
func TestDeathDuringModelLoadIsNoStrike(t *testing.T) {
	k := newFakeKernel(t)
	h := strings.Repeat("f", 64)
	for i := 0; i < 3; i++ {
		rs, prev := k.start(t)
		if len(prev.setAside) != 0 {
			t.Fatalf("set aside %v for deaths during a load", prev.setAside)
		}
		rs.track(h, true)
		rs.setLoading(true)
		k.now = k.now.Add(time.Minute)
	}
	if rec := k.marker(t); rec.Strikes[h] != 0 || !rec.ResumeAt.After(rec.Started) {
		t.Errorf("marker %+v: no strike, a pause all the same", rec)
	}
	// Loaded: a death now is the file's.
	rs, _ := k.start(t)
	rs.track(h, true)
	rs.setLoading(true)
	rs.setLoading(false)
	k.now = k.now.Add(time.Minute)
	k.start(t)
	if rec := k.marker(t); rec.Strikes[h] != 1 {
		t.Errorf("strikes %v", rec.Strikes)
	}
}

// Finding 3: after a death an 8 GB device goes one job at a time for an
// hour (recoveryFor), then back to normal.
func TestRecoveryEndsAfterAnHour(t *testing.T) {
	k := newFakeKernel(t)
	mg, _ := managerWithMock(t)
	p := memoryProfileFor(pi8GB, "", "")
	prev := recoveryFor
	recoveryFor = 20 * time.Millisecond
	mg.checkPreviousRun(k.env, p)
	k.now = k.now.Add(time.Minute)
	g := mg.checkPreviousRun(k.env, p)
	deadline := time.Now().Add(5 * time.Second)
	for g.limited.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	recoveryFor = prev
	if g.limited.Load() || g.tracking.Load() {
		t.Error("still one job at a time, recorded, after the recovery")
	}
	// A low-memory device keeps its limits.
	k.now = k.now.Add(time.Minute)
	g = mg.checkPreviousRun(k.env, memoryProfileFor(pi4GB, "", ""))
	time.Sleep(50 * time.Millisecond)
	if !g.limited.Load() {
		t.Error("a low-memory device relaxed")
	}
	mg.Stopped()
}

// Finding 8: an OOM kill of another program long ago, then a death that
// isn't memory's: the refreshed baseline knows the difference.
func TestOldOOMKillIsNotThisDeath(t *testing.T) {
	k := newFakeKernel(t)
	rs, _ := k.start(t)
	k.oomKills(4) // day 2: something else
	rs.refresh()  // the minute's look
	k.now = k.now.Add(20 * 24 * time.Hour)
	_, prev := k.start(t) // day 20: a panic
	if !prev.unclean || prev.oom {
		t.Errorf("previous run %+v", prev)
	}
	// track() looks too.
	rs, _ = k.start(t)
	k.oomKills(5)
	rs.track(strings.Repeat("a", 64), true)
	if rec := k.marker(t); rec.OOMKills != 5 {
		t.Errorf("baseline %d after a track, want 5", rec.OOMKills)
	}
}

// Finding 9: systemd stops the whole service (OOMPolicy=stop) when the
// kernel kills one of its processes - a supervised instance, an ffmpeg:
// the clean stop that follows keeps the marker, marked, and the next
// start takes it for running out of memory.
func TestStopAfterAnOOMKillInTheService(t *testing.T) {
	k := newFakeKernel(t)
	h := strings.Repeat("c", 64)
	rs, _ := k.start(t)
	rs.track(h, true) // the video whose ffmpeg was killed
	k.cgroupOOMKills(1)
	rs.stop()
	rec := k.marker(t)
	if !rec.StoppedOOM {
		t.Fatalf("marker %+v", rec)
	}
	k.cgroupOOMKills(0) // the restarted unit's new cgroup
	_, prev := k.start(t)
	if !prev.unclean || !prev.oom || prev.deaths != 1 || prev.pause != time.Minute {
		t.Errorf("previous run %+v", prev)
	}
	if rec := k.marker(t); rec.Strikes[h] != 1 {
		t.Errorf("strikes %v", rec.Strikes)
	}
	// A clean stop with no kill removes the marker.
	rs, _ = k.start(t)
	rs.stop()
	if _, err := os.Stat(k.env.statePath); !os.IsNotExist(err) {
		t.Error("a clean stop left the marker")
	}
}

// Finding 5: the backfill takes its turn - which may be a pause after a
// death - before it writes its marker, so a stop while it waits leaves
// none and the file isn't skipped by every later backfill.
func TestBackfillMarkerAfterItsTurn(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	mg, mock := managerWithMock(t)
	h := strings.Repeat("e", 64)
	marker := filepath.Join(storage, h+cNoThumbnailSuffix)
	os.Remove(marker)
	os.Remove(filepath.Join(storage, h+"_thumbnail"))
	t.Cleanup(func() { os.Remove(marker) })
	entered, block := make(chan struct{}), make(chan struct{})
	g := newProcessingGuard(nil, true)
	g.resumeAt = time.Now().Add(30 * time.Minute)
	g.sleep = func(time.Duration) {
		close(entered)
		<-block
	}
	mg.guard = g
	cols := []string{"hash", "mime", "created", "modified", "path", "size"}
	mock.ExpectQuery("select `hash` from `pending_analysis`").WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	mock.ExpectQuery("select `hash`, min").WillReturnRows(sqlmock.NewRows(cols).AddRow(h, "image/jpeg", time.Now(), time.Now(), "/Photos/a.jpg", 1000))
	go mg.BackfillMissingThumbnails(ses)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the backfill never reached the guard")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the backfill's marker is down while it waits out the pause")
	}
	// (the backfill stays parked: the process "stops" here)
}
