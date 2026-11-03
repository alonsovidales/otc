// SPDX-License-Identifier: AGPL-3.0-or-later

package nostorage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/alonsovidales/otc/alerttext"
)

// files points the package's reads at a temporary fstab, mountinfo, boot
// id and raid_watch state.
func files(t *testing.T, fstab, mountinfo, boot string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	prev := [4]string{fstabPath, mountinfoPath, bootIDPath, watchStatePath}
	fstabPath = write("fstab", fstab)
	mountinfoPath = write("mountinfo", mountinfo)
	bootIDPath = write("boot_id", boot+"\n")
	watchStatePath = filepath.Join(dir, "storage.json")
	t.Cleanup(func() { fstabPath, mountinfoPath, bootIDPath, watchStatePath = prev[0], prev[1], prev[2], prev[3] })
	return dir
}

const (
	cFstab = `# /etc/fstab: static file system information.
PARTUUID=1234-02  /               ext4    defaults,noatime  0       1
/dev/md0   /mnt/storage   ext4   defaults,nofail,x-systemd.device-timeout=3min   0   0
/dev/sdz1  /mnt/backup    ext4   noauto   0 0
/swapfile  none           swap   sw       0 0
#/dev/sdy1 /mnt           ext4   defaults 0 0
`
	cRootOnly = "22 1 179:2 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p2 rw\n"
	cMounted  = cRootOnly + "31 22 9:0 / /mnt/storage rw,relatime shared:12 - ext4 /dev/md0 rw\n"
	// otc-degraded.service's ReadOnlyPaths: the empty directory under the
	// mount point bound over itself, read-only - not the storage.
	cBound = cRootOnly + "40 22 179:2 /mnt/storage /mnt/storage ro,relatime - ext4 /dev/mmcblk0p2 rw\n"
)

func TestWantedMountIsTheDeepestFstabMountHoldingTheStorage(t *testing.T) {
	files(t, cFstab, cRootOnly, "b1")
	for path, want := range map[string]string{
		"/mnt/storage/":            "/mnt/storage",
		"/mnt/storage":             "/mnt/storage",
		"/mnt/storage/unencrypted": "/mnt/storage",
		"/mnt/storagex/":           "",
		"/mnt/backup/":             "", // noauto
		"/srv/otc/":                "", // not in fstab: a plain directory
	} {
		if got := wantedMount(path); got != want {
			t.Errorf("wantedMount(%q) = %q, want %q", path, got, want)
		}
	}
	files(t, `UUID=abc /mnt/my\040disk ext4 defaults,nofail 0 2`+"\n", cRootOnly, "b1")
	if got := wantedMount("/mnt/my disk/storage/"); got != "/mnt/my disk" {
		t.Errorf("an escaped mount point: %q", got)
	}
	prev := fstabPath
	fstabPath = filepath.Join(t.TempDir(), "none")
	defer func() { fstabPath = prev }()
	if got := wantedMount("/mnt/storage/"); got != "" {
		t.Errorf("no fstab: %q", got)
	}
}

func TestMountedIsAWholeWritableFilesystemOnTop(t *testing.T) {
	for _, c := range []struct {
		name, mountinfo string
		want            bool
	}{
		{"nothing there", cRootOnly, false},
		{"the mirror", cMounted, true},
		{"a read-only bind of the empty directory", cBound, false},
		{"the bind under the mirror", cBound + "41 40 9:0 / /mnt/storage rw,relatime - ext4 /dev/md0 rw\n", true},
		{"mounted read-only", cRootOnly + "31 22 9:0 / /mnt/storage ro,relatime - ext4 /dev/md0 ro\n", false},
	} {
		files(t, cFstab, c.mountinfo, "b1")
		if got := mounted("/mnt/storage"); got != c.want {
			t.Errorf("%s: mounted = %v", c.name, got)
		}
	}
}

func TestReady(t *testing.T) {
	storage := filepath.Join(t.TempDir(), "storage")
	if err := os.Mkdir(storage, 0o750); err != nil {
		t.Fatal(err)
	}
	ok := func() error { return nil }
	down := func() error { return errors.New("connection refused") }

	// A plain directory (no fstab line): the database decides.
	files(t, cFstab, cRootOnly, "b1")
	if why := Ready(storage, ok); why != "" {
		t.Errorf("ready: %q", why)
	}
	if why := Ready(storage, down); why != "the database doesn't answer: connection refused" {
		t.Errorf("database down: %q", why)
	}
	if why := Ready(filepath.Join(storage, "gone"), ok); why == "" {
		t.Error("a storage path that isn't there is ready")
	}
	// Under a mount fstab names: never ready until it is mounted, and the
	// database isn't asked meanwhile.
	files(t, "/dev/md0 "+storage+" ext4 defaults,nofail 0 0\n", cRootOnly, "b1")
	asked := false
	if why := Ready(storage+"/", func() error { asked = true; return nil }); why != storage+" is not mounted" || asked {
		t.Errorf("not mounted: %q, database asked %v", why, asked)
	}
	files(t, "/dev/md0 "+storage+" ext4 defaults,nofail 0 0\n", cRootOnly+"31 22 9:0 / "+storage+" rw - ext4 /dev/md0 rw\n", "b1")
	if why := Ready(storage+"/", ok); why != "" {
		t.Errorf("mounted: %q", why)
	}
}

func readRec(t *testing.T, dir string) record {
	t.Helper()
	r, ok := readRecord(dir)
	if !ok {
		t.Fatal("no record")
	}
	return r
}

func TestOutageRecordAndAlert(t *testing.T) {
	files(t, cFstab, cRootOnly, "boot-1")
	dir := t.TempDir()
	since := time.Now().Add(-90 * time.Minute)
	if err := Begin(dir, since); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, recordName)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the record: %v %v", st, err)
	}
	// A later start of the same outage (the companion restarted) keeps
	// its beginning.
	if err := Begin(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r := readRec(t, dir); r.Since != since.Unix() || r.Boot != "boot-1" {
		t.Fatalf("record %+v", r)
	}

	// Back in the same boot: until now, 90 minutes.
	var got []alerttext.Alert
	add := func(a alerttext.Alert) error { got = append(got, a); return nil }
	Report(dir, add)
	if len(got) != 1 || got[0].Title != "Your device couldn't reach its storage disks for 90 minutes" || got[0].Key != "dev.alert.storage.missing_minutes" ||
		got[0].DetailKey != "dev.alert.storage.missing_detail" {
		t.Fatalf("alert %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, recordName)); !os.IsNotExist(err) {
		t.Fatal("the record stays after its alert")
	}
	Report(dir, add)
	if len(got) != 1 {
		t.Fatal("alerted twice")
	}

	// The device restarted meanwhile: until it was last seen missing.
	r := record{V: 1, Since: time.Now().Add(-10 * 24 * time.Hour).Unix(), Last: time.Now().Add(-7 * 24 * time.Hour).Unix(), Boot: "boot-0"}
	if err := writeRecord(dir, r); err != nil {
		t.Fatal(err)
	}
	// An alert the database refuses: tried again at the next start.
	Report(dir, func(alerttext.Alert) error { return errors.New("no database") })
	if _, ok := readRecord(dir); !ok {
		t.Fatal("the record went with an alert that wasn't stored")
	}
	Report(dir, add)
	if len(got) != 2 || got[1].Title != "Your device couldn't reach its storage disks for 3 days" {
		t.Fatalf("after a restart: %+v", got)
	}

	// Touch moves the last time seen, nothing else.
	if err := Begin(dir, time.Now().Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	before := readRec(t, dir)
	before.Last -= 120
	writeRecord(dir, before)
	Touch(dir)
	if after := readRec(t, dir); after.Since != before.Since || after.Last <= before.Last {
		t.Fatalf("touch: %+v -> %+v", before, after)
	}

	// A record that can't be read says nothing, and goes.
	os.WriteFile(filepath.Join(dir, recordName), []byte("{"), 0o600)
	Report(dir, add)
	if len(got) != 2 {
		t.Fatal("an unreadable record alerted")
	}
	if _, err := os.Stat(filepath.Join(dir, recordName)); !os.IsNotExist(err) {
		t.Fatal("an unreadable record stays")
	}
}

func TestAlertForRoundsToMinutesHoursAndDays(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, "for 1 minute"},
		{80 * time.Second, "for 1 minute"},
		{59*time.Minute + 20*time.Second, "for 59 minutes"},
		{119*time.Minute + 20*time.Second, "for 119 minutes"},
		{119*time.Minute + 40*time.Second, "for 2 hours"},
		{47*time.Hour + 20*time.Minute, "for 47 hours"},
		{47*time.Hour + 40*time.Minute, "for 2 days"},
		{10 * 24 * time.Hour, "for 10 days"},
	} {
		a := AlertFor(c.d)
		if want := "Your device couldn't reach its storage disks " + c.want; a.Title != want {
			t.Errorf("%v: %q, want %q", c.d, a.Title, want)
		}
		if !a.Keyed() {
			t.Errorf("%v: not keyed", c.d)
		}
	}
}

func TestMissingSinceIsRaidWatchsWhenItMakesSense(t *testing.T) {
	files(t, cFstab, cRootOnly, "b1")
	now := time.Now()
	if got := MissingSince(now); !got.Equal(now) {
		t.Errorf("no state: %v", got)
	}
	put := func(body string) {
		if err := os.WriteFile(watchStatePath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	at := now.Add(-2 * time.Minute).Unix()
	put(`{"v":1,"missing_since":` + strconv.FormatInt(at, 10) + `}`)
	// Within this boot only (the machine's uptime, unknown on a Mac: a day).
	if up := uptime(); up > 3*time.Minute {
		if got := MissingSince(now); got.Unix() != at {
			t.Errorf("raid_watch's: %v", got)
		}
	}
	for _, body := range []string{`{"v":1,"missing_since":` + strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + `}`, `{"v":2,"missing_since":1}`, `nope`} {
		put(body)
		if got := MissingSince(now); !got.Equal(now) {
			t.Errorf("%s: %v", body, got)
		}
	}
}

// Wait returns at once when the storage is there, and serves nothing.
func TestWaitReadyAtOnce(t *testing.T) {
	files(t, cFstab, cRootOnly, "b1")
	served := false
	Wait(Options{StoragePath: t.TempDir(), Ping: func() error { return nil }, Serve: func() func() { served = true; return func() {} }})
	if served {
		t.Error("served without need")
	}
}

// Without the database for longer than the grace: the apps are told,
// the outage recorded; once it answers that stops and Wait returns.
func TestWaitServesAfterTheGraceAndStopsWhenBack(t *testing.T) {
	files(t, cFstab, cRootOnly, "b1")
	prev := [2]time.Duration{waitGrace, waitEvery}
	prevSleep := sleep
	waitGrace, waitEvery = 30*time.Millisecond, time.Millisecond
	sleep = func(d time.Duration) { time.Sleep(d) }
	defer func() { waitGrace, waitEvery, sleep = prev[0], prev[1], prevSleep }()

	dir := t.TempDir()
	start := time.Now()
	served, stopped := 0, 0
	pings := 0
	Wait(Options{
		StoragePath: t.TempDir(),
		Ping: func() error {
			pings++
			if time.Since(start) < 120*time.Millisecond {
				return errors.New("down")
			}
			return nil
		},
		Serve: func() func() {
			served++
			if _, ok := readRecord(dir); !ok {
				t.Error("serving before the outage is recorded")
			}
			return func() { stopped++ }
		},
		Dir: dir,
	})
	if served != 1 || stopped != 1 {
		t.Fatalf("served %d, stopped %d", served, stopped)
	}
	if time.Since(start) < 120*time.Millisecond {
		t.Fatal("returned before the database answered")
	}
	r := readRec(t, dir)
	if r.Since != start.Unix() && r.Since != start.Unix()-1 {
		t.Errorf("the outage began at %d, Wait at %d", r.Since, start.Unix())
	}
	raw, _ := json.Marshal(r)
	t.Log("record:", string(raw), "pings:", pings)
}
