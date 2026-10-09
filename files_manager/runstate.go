// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
)

// A process the kernel kills - out of memory, most likely - logs nothing:
// systemd starts it again three seconds later, and if what killed it is
// still to do (the uploads a phone keeps sending, the analysis a restart
// resumes) it dies again, every minute or two, while the owner sees a
// device that works between deaths. So every instance leaves a marker
// where it runs while it runs, and only a clean stop removes it: found at
// a start, the previous run died. If the kernel's count of OOM kills rose
// meanwhile, it ran out of memory, and the owner gets one Alert that keeps
// count. The first hour of the run that follows processes one file at a
// time and records which file it is on - content being processed at two
// deaths is set aside rather than retried forever - and when the death
// looked like processing's (out of memory, or a file in flight) its
// analysis waits a while first, longer after each such death.

// cRunStateFile is the marker, in the instance's working directory: the
// service's StateDirectory (/var/lib/otc) for the primary, the user's home
// for a supervised instance. Not the RuntimeDirectory: systemd empties it
// whenever the unit stops, a crash included.
const cRunStateFile = ".otc-run"

// cCrashSeries: a run that dies within this long of starting continues the
// series of deaths before it (the count, the pause, the strikes); one that
// lasted longer starts a new series. It is also how long a run after a
// death goes one job at a time.
const cCrashSeries = time.Hour

// recoveryFor is how long a run after a death goes one job at a time and
// records what it processes (cCrashSeries; a variable for tests).
var recoveryFor = cCrashSeries

// cStrikesToSetAside: content being processed at this many deaths is set
// aside.
const cStrikesToSetAside = 2

// cBaselineRefresh is how often a run looks at the OOM kill counters and,
// when they moved, records them as its new baseline: a kill of another
// program long ago must not make a later death look like memory's.
const cBaselineRefresh = time.Minute

// processingPause is how long analysis waits after the n-th death in a row
// that looked like processing's: 1, 2, 4, 8, 16 minutes, then 30.
func processingPause(level int) time.Duration {
	switch {
	case level <= 0:
		return 0
	case level > 5:
		return 30 * time.Minute
	}
	return time.Minute << (level - 1)
}

// runRecord is the marker's content.
type runRecord struct {
	// Boot is the kernel's boot id: the counters below are only
	// comparable within one boot.
	Boot    string    `json:"boot"`
	Started time.Time `json:"started"`
	// OOMKills is /proc/vmstat's oom_kill and CgroupOOMKills the service
	// cgroup's (memory.events) as last looked at (cBaselineRefresh); -1
	// unknown.
	OOMKills       int64 `json:"oom_kills"`
	CgroupOOMKills int64 `json:"cgroup_oom_kills"`
	// Deaths is how many runs in a row died before this one, OOMDeaths how
	// many of those ran out of memory, Since when the first of them ended,
	// Alert the Alert row counting them, Level the pause's step.
	Deaths    int       `json:"deaths,omitempty"`
	OOMDeaths int       `json:"oom_deaths,omitempty"`
	Since     time.Time `json:"since,omitzero"`
	Alert     string    `json:"alert,omitempty"`
	Level     int       `json:"level,omitempty"`
	// ResumeAt is when this run's analysis may start (after a death).
	ResumeAt time.Time `json:"resume_at,omitzero"`
	// InFlight is the content being processed now; Strikes counts the
	// deaths each hash was being processed at.
	InFlight []string       `json:"in_flight,omitempty"`
	Strikes  map[string]int `json:"strikes,omitempty"`
	// Loading: a model is loading - a death now is the load's, not the
	// files' in flight.
	Loading bool `json:"loading,omitempty"`
	// StoppedOOM: the run was stopped (systemd's OOMPolicy=stop) after the
	// kernel killed a process of the service for memory - a supervised
	// instance or an ffmpeg.
	StoppedOOM bool `json:"stopped_oom,omitempty"`
}

// runEnv is where a start reads the kernel's facts and keeps the marker;
// tests point it at fake files.
type runEnv struct {
	statePath  string // the marker
	bootIDPath string // /proc/sys/kernel/random/boot_id
	vmstatPath string // /proc/vmstat
	cgroupPath string // /proc/self/cgroup
	cgroupRoot string // /sys/fs/cgroup
	now        func() time.Time
}

func defaultRunEnv() (runEnv, error) {
	wd, err := os.Getwd()
	if err != nil {
		return runEnv{}, err
	}
	return runEnv{
		statePath:  filepath.Join(wd, cRunStateFile),
		bootIDPath: "/proc/sys/kernel/random/boot_id",
		vmstatPath: "/proc/vmstat",
		cgroupPath: "/proc/self/cgroup",
		cgroupRoot: "/sys/fs/cgroup",
		now:        time.Now,
	}, nil
}

// previousRun is what a start found out about the run before it.
type previousRun struct {
	// unclean: it didn't stop cleanly (or stopped for memory). sameBoot:
	// it ran in this boot (else the device itself went down - a power
	// cut, a reboot - and nothing else is known). oom: the kernel's OOM
	// killer killed something of it, or since the counters were last
	// looked at.
	unclean, sameBoot, oom bool
	// deaths and oomDeaths count the series, this death included; since is
	// when it began.
	deaths, oomDeaths int
	since             time.Time
	// pause is how long this run's analysis waits: 0 unless the death
	// looked like processing's (oom, or content in flight) - and not
	// longer when it came during the dead run's own pause.
	pause time.Duration
	// setAside is the content processed at cStrikesToSetAside deaths.
	setAside []string
}

// runState is this run's marker.
type runState struct {
	env runEnv
	mu  sync.Mutex
	rec runRecord
	f   *os.File
	// size is the longest content written: the marker is rewritten in
	// place, padded, never truncated - ext4 writes a file truncated and
	// rewritten out at once, and the in-flight hashes change per file.
	size int
	// startCgroup is the cgroup's OOM kill count when this run started -
	// not refreshed, unlike rec's.
	startCgroup int64
	stopped     bool
	quit        chan struct{}
}

// startRun reads what the previous run left and writes this run's marker.
// An error means the facts can't be read here (not Linux): no marker.
func startRun(env runEnv) (*runState, previousRun, error) {
	raw, err := os.ReadFile(env.bootIDPath)
	if err != nil {
		return nil, previousRun{}, err
	}
	now := env.now()
	rec := runRecord{
		Boot:           strings.TrimSpace(string(raw)),
		Started:        now,
		OOMKills:       vmstatOOMKills(env.vmstatPath),
		CgroupOOMKills: cgroupOOMKills(env.cgroupPath, env.cgroupRoot),
	}
	var prev previousRun
	if old, err := os.ReadFile(env.statePath); err == nil {
		prev = rec.follow(old, now)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, previousRun{}, err
	}
	f, err := os.OpenFile(env.statePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600) // perms: rw-------
	if err != nil {
		return nil, prev, err
	}
	rs := &runState{env: env, rec: rec, f: f, startCgroup: rec.CgroupOOMKills, quit: make(chan struct{})}
	rs.mu.Lock()
	err = rs.writeLocked()
	rs.mu.Unlock()
	return rs, prev, err
}

// follow fills rec, the new run's record, from the previous run's marker
// (old) and says what became of that run.
func (rec *runRecord) follow(old []byte, now time.Time) previousRun {
	prev := previousRun{unclean: true}
	var last runRecord
	if json.Unmarshal(old, &last) != nil {
		// Cut short as it was written: died, nothing else known.
		last = runRecord{OOMKills: -1, CgroupOOMKills: -1}
	}
	prev.sameBoot = last.Boot != "" && last.Boot == rec.Boot
	if !prev.sameBoot {
		return prev
	}
	prev.oom = last.StoppedOOM || rose(last.OOMKills, rec.OOMKills) || rose(last.CgroupOOMKills, rec.CgroupOOMKills)
	if now.Sub(last.Started) < cCrashSeries && last.Deaths > 0 && !last.Since.IsZero() {
		// It died young, after deaths of its own: the same series.
		rec.Deaths, rec.OOMDeaths, rec.Since, rec.Alert, rec.Level = last.Deaths, last.OOMDeaths, last.Since, last.Alert, last.Level
		rec.Strikes = last.Strikes
	} else {
		// The first death of a series: about now, systemd restarts a
		// service within seconds.
		rec.Since = now
	}
	rec.Deaths++
	if prev.oom {
		rec.OOMDeaths++
	}
	// The pause is for deaths that look like processing's. Nothing in
	// flight and memory to spare - a panic in a request, say - pausing
	// the analysis wouldn't help; a death during the dead run's own pause
	// doesn't make the next one longer.
	if prev.oom || len(last.InFlight) > 0 {
		duringPause := !last.ResumeAt.IsZero() && now.Before(last.ResumeAt)
		if !duringPause || rec.Level == 0 {
			rec.Level++
		}
		prev.pause = processingPause(rec.Level)
	}
	// A death while a model loaded (its peak) is the load's: the files in
	// flight then get no strike.
	if !last.Loading {
		for _, h := range last.InFlight {
			if rec.Strikes == nil {
				rec.Strikes = map[string]int{}
			}
			rec.Strikes[h]++
			if rec.Strikes[h] >= cStrikesToSetAside {
				prev.setAside = append(prev.setAside, h)
				delete(rec.Strikes, h)
			}
		}
	}
	rec.ResumeAt = now.Add(prev.pause)
	prev.deaths, prev.oomDeaths, prev.since = rec.Deaths, rec.OOMDeaths, rec.Since
	return prev
}

// rose: a counter known both times went up.
func rose(before, after int64) bool {
	return before >= 0 && after > before
}

// writeLocked rewrites the marker in place. rs.mu held.
func (rs *runState) writeLocked() error {
	if rs.stopped {
		return nil
	}
	data, err := json.Marshal(rs.rec)
	if err != nil {
		return err
	}
	if n := len(data); n < rs.size {
		data = append(data, strings.Repeat(" ", rs.size-n)...)
	} else {
		rs.size = n
	}
	_, err = rs.f.WriteAt(data, 0)
	return err
}

// refreshLocked records the OOM kill counters as they are now; it reports
// whether they moved. rs.mu held.
func (rs *runState) refreshLocked() bool {
	vm := vmstatOOMKills(rs.env.vmstatPath)
	cg := cgroupOOMKills(rs.env.cgroupPath, rs.env.cgroupRoot)
	if vm == rs.rec.OOMKills && cg == rs.rec.CgroupOOMKills {
		return false
	}
	rs.rec.OOMKills, rs.rec.CgroupOOMKills = vm, cg
	return true
}

// refresh is refreshLocked, writing the marker when the counters moved.
func (rs *runState) refresh() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.refreshLocked() {
		if err := rs.writeLocked(); err != nil {
			log.Debug("could not update the run marker:", err)
		}
	}
}

// watch refreshes the counters every interval until the run stops.
func (rs *runState) watch(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-rs.quit:
			return
		case <-t.C:
			rs.refresh()
		}
	}
}

// track records hash as being processed (on) or done with.
func (rs *runState) track(hash string, on bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	i := slices.Index(rs.rec.InFlight, hash)
	switch {
	case on && i < 0:
		rs.rec.InFlight = append(rs.rec.InFlight, hash)
	case !on && i >= 0:
		rs.rec.InFlight = slices.Delete(rs.rec.InFlight, i, i+1)
	default:
		return
	}
	rs.refreshLocked()
	if err := rs.writeLocked(); err != nil {
		log.Debug("could not update the run marker:", err)
	}
}

// setLoading records that a model is loading (on) or done.
func (rs *runState) setLoading(on bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.rec.Loading == on {
		return
	}
	rs.rec.Loading = on
	if err := rs.writeLocked(); err != nil {
		log.Debug("could not update the run marker:", err)
	}
}

// setAlert records the Alert row counting the series.
func (rs *runState) setAlert(id string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.rec.Alert = id
	if err := rs.writeLocked(); err != nil {
		log.Error("could not update the run marker:", err)
	}
}

// haltLocked stops the watcher and closes the marker, leaving it where it
// is. rs.mu held.
func (rs *runState) haltLocked() {
	if rs.stopped {
		return
	}
	rs.stopped = true
	close(rs.quit)
	rs.f.Close()
}

// stop is the run's clean end: the marker goes - unless the kernel killed
// a process of the service for memory while it ran (systemd then stops
// the whole unit, OOMPolicy=stop's default), which the next start must
// take for what it is.
func (rs *runState) stop() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.stopped {
		return
	}
	if cg := cgroupOOMKills(rs.env.cgroupPath, rs.env.cgroupRoot); rose(rs.startCgroup, cg) {
		rs.rec.StoppedOOM = true
		if err := rs.writeLocked(); err != nil {
			log.Error("could not update the run marker:", err)
		}
		log.Error("stopping after the kernel killed a process of this service for memory: the next start takes it for running out of memory")
		rs.haltLocked()
		return
	}
	rs.haltLocked()
	if err := os.Remove(rs.env.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Error("could not remove the run marker:", err)
	}
}

// vmstatOOMKills is the kernel's count of OOM kills since boot, -1 when
// it can't be read.
func vmstatOOMKills(path string) int64 {
	return counterIn(path, "oom_kill")
}

// cgroupOOMKills is the oom_kill count of this process's cgroup (v2
// memory.events): kills of its processes, -1 when it can't be read.
func cgroupOOMKills(cgroupPath, root string) int64 {
	raw, err := os.ReadFile(cgroupPath)
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok && rest != "" {
			return counterIn(filepath.Join(root, filepath.Clean("/"+rest), "memory.events"), "oom_kill")
		}
	}
	return -1
}

// counterIn is the value of a "name N" line of a /proc-style file, -1
// when there is none.
func counterIn(path, name string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[0] == name {
			if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return n
			}
		}
	}
	return -1
}

// clockTime is t as the Alerts' lines show times: "15:04" today,
// "2 Jan 15:04" another day.
func clockTime(t, now time.Time) string {
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

// memoryAlertTitle is the Alert's one line for prev, a series of runs
// that ran out of memory.
func memoryAlertTitle(prev previousRun, now time.Time) string {
	if prev.oomDeaths <= 1 {
		return "Your device ran out of memory and restarted"
	}
	return fmt.Sprintf("Your device ran out of memory and restarted %d times since %s", prev.oomDeaths, clockTime(prev.since, now))
}

// memoryAlertHint is the Alert's detail, in plain words.
func memoryAlertHint(p MemoryProfile) string {
	size := "This device"
	if p.Total > 0 {
		size = fmt.Sprintf("This device has %d GB of memory", p.NominalGB())
	}
	switch {
	case p.Low && p.Total > 0 && p.NominalGB() < 8:
		return size + ", less than the 8 GB recommended: it processes one photo at a time to fit (low-memory mode), and after a restart it waits a while before it goes on. If this keeps happening, send the logs from Settings > Logs."
	case p.Low:
		return size + ": it processes one photo at a time (low-memory mode), and after a restart it waits a while before it goes on. If this keeps happening, send the logs from Settings > Logs."
	}
	return size + ". After a restart it goes on processing one file at a time for an hour. If this keeps happening, send the logs from Settings > Logs."
}

// checkPreviousRun starts this run's marker, reports how the previous run
// ended and, when it died, makes this run's processing gentler (the guard
// it returns, nil when nothing needs to change).
func (mg *Manager) checkPreviousRun(env runEnv, p MemoryProfile) *processingGuard {
	if mg.run != nil {
		mg.run.mu.Lock()
		mg.run.haltLocked() // a test's previous run in the same Manager
		mg.run.mu.Unlock()
	}
	rs, prev, err := startRun(env)
	if err != nil {
		if rs == nil {
			log.Debug("no run marker:", err)
		} else {
			log.Error("could not write the run marker:", err)
		}
	}
	mg.run = rs
	if rs == nil {
		return mg.profileGuard(p, nil)
	}
	go rs.watch(cBaselineRefresh)
	now := env.now()
	switch {
	case !prev.unclean:
	case !prev.sameBoot:
		log.Info("the previous run ended unexpectedly, with the device itself (a power cut or a restart)")
	case prev.oom:
		log.Error(fmt.Sprintf("the previous run ran out of memory: the kernel stopped it or a process of it (%d time(s) in a row since %s)", prev.oomDeaths, clockTime(prev.since, now)))
		mg.raiseMemoryAlert(rs, prev, p, now)
	default:
		log.Error(fmt.Sprintf("the previous run ended unexpectedly (%d time(s) in a row since %s; not out of memory as far as the kernel says)", prev.deaths, clockTime(prev.since, now)))
	}
	for _, hash := range prev.setAside {
		mg.setAside(hash)
	}
	if !prev.unclean || !prev.sameBoot {
		return mg.profileGuard(p, rs)
	}
	// One job at a time, recorded, for an hour (the low-memory profile's
	// for good), the analysis after the pause.
	g := newProcessingGuard(rs.track, p.Low)
	g.resumeAt = now.Add(prev.pause)
	if prev.pause > 0 {
		log.Info(fmt.Sprintf("analysis resumes in %d minute(s); processing goes one file at a time for an hour", int(prev.pause.Minutes())))
	} else {
		log.Info("processing goes one file at a time for an hour")
	}
	if !p.Low {
		time.AfterFunc(recoveryFor, func() {
			g.relax()
			log.Info("an hour without stopping: processing is back to normal")
		})
	}
	return g
}

// raiseMemoryAlert raises - or, for a series already raised, updates - the
// one Alert that counts the deaths (issue #64's Alerts; never a push). The
// row stands alone: no other error joins it.
func (mg *Manager) raiseMemoryAlert(rs *runState, prev previousRun, p MemoryProfile, now time.Time) {
	detail := ""
	if rs.rec.Alert == "" {
		detail = memoryAlertHint(p)
	}
	id, err := mg.dao.UpsertErrorNotification(rs.rec.Alert, memoryAlertTitle(prev, now), detail)
	if err != nil {
		log.Error("error recording the alert:", err)
		return
	}
	if id != rs.rec.Alert {
		rs.setAlert(id)
	}
}

// profileGuard is the guard p asks for: one job at a time when low, the
// in-flight content recorded on rs when there is one. nil for the normal
// profile.
func (mg *Manager) profileGuard(p MemoryProfile, rs *runState) *processingGuard {
	if !p.Low {
		return nil
	}
	if rs == nil {
		return newProcessingGuard(nil, true)
	}
	return newProcessingGuard(rs.track, true)
}

// Stopped is the clean end of the run: the marker goes, so the next start
// knows this one didn't die.
func (mg *Manager) Stopped() {
	if mg != nil && mg.run != nil {
		mg.run.stop()
	}
}
