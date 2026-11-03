// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nostorage is the device without its storage (issue #207): the
// disks unplugged, switched off or slow at boot, a mirror that isn't
// assembled, MariaDB unable to start on them, a read-only data directory.
// The device used to wait in silence (dao.Init's loop, or otc.service not
// started at all), and every app said "device unreachable". Now, after
// Grace without its storage, the device answers every request with the
// error_code storage_unavailable, on its own port and through the bridge
// (websocket.Unavailable, api.ServeUnavailable), and starts normally once
// the storage is back.
//
// Two ways in. The service waits here before anything touches the storage
// (Wait, from bin/otc.go): the storage path mounted when fstab says it is
// a mount, writable, and the database answering. And when otc.service
// can't start at all (its RequiresMountsFor), the root raid_watch.py
// starts otc-degraded.service, `otc <env> degraded`, which serves the same
// without ever opening the database, and starts otc again once the
// storage is mounted (otc.service conflicts with it).
//
// Nothing here writes under the storage path: the outage is recorded in
// the working directory, on the boot disk (Begin, Touch), and the next
// full start turns it into an Alert saying how long it lasted (Report).
package nostorage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	"golang.org/x/sys/unix"
)

// Grace is how long the device waits for its storage before it tells the
// apps: a slow disk at boot, or MariaDB still starting, is not an outage.
const Grace = 60 * time.Second

// Wait's clock: Grace, and how often it looks again. Variables for the
// tests.
var (
	waitGrace = Grace
	waitEvery = 5 * time.Second
	sleep     = time.Sleep
)

// The files Ready reads; variables for the tests.
var (
	fstabPath     = "/etc/fstab"
	mountinfoPath = "/proc/self/mountinfo"
	bootIDPath    = "/proc/sys/kernel/random/boot_id"
	// watchStatePath is what raid_watch.py writes while the storage is
	// missing: {"v":1,"missing_since":<unix>}, root's, 0644.
	watchStatePath = "/run/otc-raid-watch/storage.json"
)

// Ready is "" when the storage can be used, else why not (English, for
// the log): the storage path is under a mount fstab names that isn't
// mounted, it can't be written, or the database doesn't answer.
func Ready(storagePath string, ping func() error) (why string) {
	if storagePath == "" {
		return ""
	}
	if mp := wantedMount(storagePath); mp != "" && !mounted(mp) {
		return mp + " is not mounted" // i18n-ignore: a log line
	}
	if err := unix.Access(storagePath, unix.W_OK|unix.X_OK); err != nil {
		return "the storage " + storagePath + " can't be written: " + err.Error() // i18n-ignore: a log line
	}
	if ping != nil {
		if err := ping(); err != nil {
			return "the database doesn't answer: " + err.Error() // i18n-ignore: a log line
		}
	}
	return ""
}

// wantedMount is the mount point fstab names for path - the deepest one
// holding it, never "/" - or "" when the storage is a plain directory
// (OTC_SKIP_RAID, a container's volume, a computer without fstab). A
// noauto line, swap and a commented line don't count.
func wantedMount(path string) string {
	raw, err := os.ReadFile(fstabPath)
	if err != nil {
		return ""
	}
	path = filepath.Clean(path)
	best := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 || f[2] == "swap" {
			continue
		}
		if len(f) >= 4 && hasOption(f[3], "noauto") {
			continue
		}
		mp := filepath.Clean(unescape(f[1]))
		if mp == "/" || !filepath.IsAbs(mp) {
			continue
		}
		if (path == mp || strings.HasPrefix(path, mp+"/")) && len(mp) > len(best) {
			best = mp
		}
	}
	return best
}

func hasOption(opts, name string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == name {
			return true
		}
	}
	return false
}

// mounted is whether a whole filesystem is mounted at mp - its root, not
// a directory of another one bound there (the read-only bind
// otc-degraded.service puts over the empty mount point is not the
// storage) - and writable.
func mounted(mp string) bool {
	f, err := os.Open(mountinfoPath)
	if err != nil {
		return false
	}
	defer f.Close()
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		// id parent major:minor root mountpoint options ... - fstype source superoptions
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 || filepath.Clean(unescape(fields[4])) != mp {
			continue
		}
		// The last line for a mount point is what is on top.
		found = unescape(fields[3]) == "/" && !hasOption(fields[5], "ro")
	}
	return found
}

// unescape undoes fstab's and mountinfo's octal escapes (\040 a space).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Options are Wait's.
type Options struct {
	// StoragePath is [otc] storage-path.
	StoragePath string
	// Ping asks the database.
	Ping func() error
	// Serve starts answering storage_unavailable and returns what stops
	// it, which Wait calls before it returns.
	Serve func() (stop func())
	// Dir is where the outage is recorded (the working directory, on the
	// boot disk); "" records nothing (an additional user's instance: the
	// main one tells the owner).
	Dir string
}

// Wait returns once the storage can be used (Ready). Until then it looks
// every few seconds, and after Grace it serves storage_unavailable
// (o.Serve) and records the outage in o.Dir, stopping both before it
// returns. A storage ready at the first look returns at once.
func Wait(o Options) {
	start := time.Now()
	var stop func()
	lastWhy := ""
	lastTouch := time.Time{}
	for {
		why := Ready(o.StoragePath, o.Ping)
		if why == "" {
			if stop != nil {
				stop()
				log.Info("the storage is back after", time.Since(start).Round(time.Second), "- starting normally")
			} else if lastWhy != "" {
				log.Info("the storage is ready")
			}
			return
		}
		if why != lastWhy {
			log.Error("waiting for the storage:", why)
			lastWhy = why
		}
		if stop == nil && time.Since(start) >= waitGrace {
			log.Info("no storage for", waitGrace, "- telling the apps (storage_unavailable) until it is back")
			if o.Dir != "" {
				if err := Begin(o.Dir, start); err != nil {
					log.Error("could not record the outage:", err)
				}
				lastTouch = time.Now()
			}
			if o.Serve != nil {
				stop = o.Serve()
			} else {
				stop = func() {}
			}
		} else if stop != nil && o.Dir != "" && time.Since(lastTouch) >= time.Minute {
			Touch(o.Dir)
			lastTouch = time.Now()
		}
		sleep(waitEvery)
	}
}

// recordName is the outage's record in the working directory.
const recordName = ".storage-missing.json"

// record is an outage: since when the storage was missing, when it was
// last seen missing, and in which boot.
type record struct {
	V     int    `json:"v"`
	Since int64  `json:"since"`
	Last  int64  `json:"last"`
	Boot  string `json:"boot"`
}

func bootID() string {
	raw, err := os.ReadFile(bootIDPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func readRecord(dir string) (record, bool) {
	f, err := os.OpenFile(filepath.Join(dir, recordName), os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return record{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return record{}, false
	}
	var r record
	if json.Unmarshal(raw, &r) != nil || r.V != 1 || r.Since <= 0 {
		return record{}, false
	}
	return r, true
}

// writeRecord writes r beside the record, flushes it and renames it over
// (0600: only the service reads it).
func writeRecord(dir string, r record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, recordName+".tmp.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(dir, recordName))
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// Begin records that the storage has been missing since since. An outage
// already recorded - the companion restarted, or a restart of the device
// with the disks still out - keeps its start.
func Begin(dir string, since time.Time) error {
	now := time.Now()
	r, ok := readRecord(dir)
	if !ok || r.Since > since.Unix() {
		r = record{V: 1, Since: since.Unix()}
	}
	r.Last, r.Boot = now.Unix(), bootID()
	return writeRecord(dir, r)
}

// Touch notes that the storage is still missing (once a minute while it
// is): after a restart of the device the outage is known to have lasted
// at least until then.
func Touch(dir string) {
	r, ok := readRecord(dir)
	if !ok {
		return
	}
	r.Last, r.Boot = time.Now().Unix(), bootID()
	if err := writeRecord(dir, r); err != nil {
		log.Error("could not note the outage:", err)
	}
}

// Report turns a recorded outage into its Alert, once the storage is back
// (the main instance's full start). Its end is now when the device didn't
// restart since, else when it was last seen missing. The record goes once
// the Alert is stored; one that can't be stored is tried at the next start.
func Report(dir string, add func(alerttext.Alert) error) {
	r, ok := readRecord(dir)
	if !ok {
		os.Remove(filepath.Join(dir, recordName)) // an unreadable one says nothing
		return
	}
	end := time.Now().Unix()
	if r.Boot == "" || r.Boot != bootID() {
		end = r.Last
	}
	d := time.Duration(max(end-r.Since, 0)) * time.Second
	if err := add(AlertFor(d)); err != nil {
		log.Error("could not add the storage outage's alert:", err)
		return
	}
	log.Info("the storage had been missing for", d.Round(time.Second))
	if err := os.Remove(filepath.Join(dir, recordName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Error("could not remove the outage's record:", err)
	}
}

// AlertFor is the Alert for an outage of d: in minutes (at least one) up
// to two hours, then hours up to two days, then days.
func AlertFor(d time.Duration) alerttext.Alert {
	var title i18n.Msg
	switch mins, hours := math.Round(d.Minutes()), math.Round(d.Hours()); {
	case hours >= 48:
		title = i18n.DevAlertStorageMissingDays(int(math.Round(d.Hours() / 24)))
	case mins >= 120:
		title = i18n.DevAlertStorageMissingHours(int(hours))
	default:
		title = i18n.DevAlertStorageMissingMinutes(max(1, int(mins)))
	}
	return alerttext.New(title, i18n.DevAlertStorageMissingDetail())
}

// MissingSince is when raid_watch.py first saw the storage missing
// (watchStatePath), or now when it doesn't say: the companion's start of
// the outage. A time in the future or before this boot is not taken.
func MissingSince(now time.Time) time.Time {
	raw, err := os.ReadFile(watchStatePath)
	if err != nil || len(raw) > 4096 {
		return now
	}
	var st struct {
		V            int   `json:"v"`
		MissingSince int64 `json:"missing_since"`
	}
	if json.Unmarshal(bytes.TrimSpace(raw), &st) != nil || st.V != 1 || st.MissingSince <= 0 {
		return now
	}
	t := time.Unix(st.MissingSince, 0)
	if t.After(now) || now.Sub(t) > uptime() {
		return now
	}
	return t
}

// uptime is how long the machine has been up; a day when unknown.
func uptime() time.Duration {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 24 * time.Hour
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return 24 * time.Hour
	}
	s, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 24 * time.Hour
	}
	return time.Duration(s*float64(time.Second)) + time.Minute
}
