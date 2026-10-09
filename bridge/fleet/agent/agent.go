// SPDX-License-Identifier: AGPL-3.0-or-later

// Package agent is otc-fleet-agent (bridge/fleetagent): it runs on every
// server of the cluster, collects what the Fleet tab shows about the host
// - from /proc, statfs, /proc/mdstat, systemctl, adjtimex, MySQL's and
// Redis's status - and writes it to Redis as one JSON snapshot
// (fleet.KeyHostPrefix + name) every interval. It only reads: it changes
// nothing on the host, listens on nothing, and runs as an unprivileged
// systemd DynamicUser (bridge/cluster/fleet/otc-fleet-agent.service).
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/bridge/fleet"
	"github.com/redis/go-redis/v9"
)

// Config is the agent's command line (fleetagent/main.go).
type Config struct {
	Name     string
	Roles    []string // bridge, mysql, redis, certbot
	Interval time.Duration

	RedisAddr, RedisUser, RedisPass string
	MySQLAddr, MySQLUser, MySQLPass string

	Certs []string // certificate files whose expiry to report
	Units []string // more units to watch than the roles' own
}

// unitsFor is the services each role needs; every host has the tunnel
// and the firewall.
var unitsFor = map[string][]string{
	"":        {"wg-quick@wg0", "ufw"},
	"bridge":  {"otc_bridge"},
	"mysql":   {"mysql"},
	"redis":   {"redis-server"},
	"certbot": {"certbot.timer"},
}

// Agent collects and publishes snapshots.
type Agent struct {
	cfg   Config
	rdb   *redis.Client
	db    *sql.DB // nil unless the host has the mysql role
	units []string

	prevCPU  CPUTimes
	prevNet  map[string]NetCounters
	prevTime time.Time

	updMu   sync.Mutex
	updates *fleet.Updates
	updErr  string

	failing bool // the last publish failed: log only the changes
}

// New builds an agent; nothing is contacted yet.
func New(cfg Config) *Agent {
	if cfg.Interval <= 0 {
		cfg.Interval = fleet.DefaultInterval
	}
	a := &Agent{cfg: cfg}
	if cfg.RedisAddr != "" {
		a.rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.RedisAddr,
			Username: cfg.RedisUser,
			Password: cfg.RedisPass,
			// RESP2 and no CLIENT SETINFO: the README's Redis user may run
			// only what the agent needs (AUTH, PING, SET, HSET, INFO).
			Protocol:        2,
			DisableIdentity: true,
			DialTimeout:     3 * time.Second,
			ReadTimeout:     3 * time.Second,
			WriteTimeout:    3 * time.Second,
			PoolSize:        2,
		})
	}
	if a.has("mysql") && cfg.MySQLAddr != "" {
		db, err := sql.Open("mysql", mysqlDSN(cfg.MySQLAddr, cfg.MySQLUser, cfg.MySQLPass))
		if err == nil {
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			db.SetConnMaxLifetime(10 * time.Minute)
			a.db = db
		}
	}
	seen := map[string]bool{}
	for _, r := range append([]string{""}, cfg.Roles...) {
		for _, u := range unitsFor[r] {
			if !seen[u] {
				seen[u] = true
				a.units = append(a.units, u)
			}
		}
	}
	for _, u := range cfg.Units {
		if u != "" && !seen[u] {
			seen[u] = true
			a.units = append(a.units, u)
		}
	}
	return a
}

func (a *Agent) has(role string) bool { return slices.Contains(a.cfg.Roles, role) }

// Run publishes a snapshot every interval until ctx ends.
func (a *Agent) Run(ctx context.Context) {
	go a.updatesLoop(ctx)
	// A first CPU and network sample, so the first snapshot has rates.
	a.Sample()
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}
	t := time.NewTicker(a.cfg.Interval)
	defer t.Stop()
	for {
		a.publish(ctx, a.Collect(ctx))
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Agent) publish(ctx context.Context, s *fleet.HostSnapshot) {
	err := Publish(ctx, a.rdb, s)
	switch {
	case err != nil && !a.failing:
		log.Println("could not publish the snapshot (logged again once it works):", err)
		a.failing = true
	case err == nil && a.failing:
		log.Println("publishing again")
		a.failing = false
	}
}

// Publish writes s with its TTL and records when its host last reported.
func Publish(ctx context.Context, rdb *redis.Client, s *fleet.HostSnapshot) error {
	if rdb == nil {
		return errors.New("no Redis configured")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pipe := rdb.Pipeline()
	pipe.Set(ctx, fleet.KeyHostPrefix+s.Name, b, fleet.SnapshotTTL)
	pipe.HSet(ctx, fleet.KeyKnownHosts, s.Name, strconv.FormatInt(s.Time.Unix(), 10))
	_, err = pipe.Exec(ctx)
	return err
}

// Sample takes the counters CPU and network rates are computed from; Run
// does it, -once does it a second before its one Collect.
func (a *Agent) Sample() {
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		if c, _, err := ParseCPUStat(string(b)); err == nil {
			a.prevCPU = c
		}
	}
	if b, err := os.ReadFile("/proc/net/dev"); err == nil {
		a.prevNet = ParseNetDev(string(b))
	}
	a.prevTime = time.Now()
}

// Collect builds one snapshot. Whatever can't be read is left out and
// named in Errors; nothing here fails the whole snapshot.
func (a *Agent) Collect(ctx context.Context) *fleet.HostSnapshot {
	now := time.Now()
	s := &fleet.HostSnapshot{
		Name:     a.cfg.Name,
		Time:     now.UTC(),
		Interval: int(a.cfg.Interval / time.Second),
		Version:  fleet.Version,
		Expect:   append([]string{}, a.cfg.Roles...),
		CPUs:     runtime.NumCPU(),
		Disks:    []fleet.Disk{},
		Net:      []fleet.NetIf{},
		Certs:    []fleet.Cert{},
		Errors:   []string{},
	}
	errf := func(what string, err error) {
		s.Errors = append(s.Errors, what+": "+short(err.Error()))
	}
	read := func(path string) (string, bool) {
		b, err := os.ReadFile(path)
		if err != nil {
			errf(path, err)
			return "", false
		}
		return string(b), true
	}

	if t, ok := read("/proc/sys/kernel/osrelease"); ok {
		s.Kernel = strings.TrimSpace(t)
	}
	if t, err := os.ReadFile("/etc/os-release"); err == nil {
		s.OS = ParseOSRelease(string(t))
	}
	if t, ok := read("/proc/uptime"); ok {
		s.Uptime, _ = ParseUptime(t)
	}
	if t, ok := read("/proc/loadavg"); ok {
		if l, err := ParseLoadavg(t); err == nil {
			s.Load = l
		}
	}
	secs := now.Sub(a.prevTime).Seconds()
	if t, ok := read("/proc/stat"); ok {
		if c, btime, err := ParseCPUStat(t); err == nil {
			s.CPU = CPUPercent(a.prevCPU, c)
			a.prevCPU = c
			if btime > 0 {
				s.BootTime = time.Unix(btime, 0).UTC()
			}
		}
	}
	if t, ok := read("/proc/net/dev"); ok {
		cur := ParseNetDev(t)
		s.Net = NetRates(a.prevNet, cur, secs)
		a.prevNet = cur
	}
	a.prevTime = now
	if t, ok := read("/proc/meminfo"); ok {
		if m, err := ParseMeminfo(t); err == nil {
			s.Memory = m
		} else {
			errf("memory", err)
		}
	}
	if t, ok := read("/proc/self/mounts"); ok {
		for _, m := range ParseMounts(t) {
			d, err := diskUsage(m)
			if err != nil {
				errf("disk "+m.Point, err)
				continue
			}
			s.Disks = append(s.Disks, d)
		}
	}
	// No md driver loaded means no /proc/mdstat: no RAID, not an error.
	if b, err := os.ReadFile("/proc/mdstat"); err == nil {
		s.RAID = ParseMdstat(string(b))
	} else {
		s.RAID = &fleet.RAID{Arrays: []fleet.MDArray{}}
	}
	if c, err := clock(); err == nil {
		s.Clock = c
	} else {
		errf("clock", err)
	}

	if _, err := os.Stat("/var/run/reboot-required"); err == nil {
		s.RebootRequired = true
		if b, err := os.ReadFile("/var/run/reboot-required.pkgs"); err == nil {
			s.RebootPackages = len(strings.Fields(string(b)))
		}
	}
	a.updMu.Lock()
	s.Updates = a.updates
	if a.updErr != "" {
		s.Errors = append(s.Errors, "updates: "+a.updErr)
	}
	a.updMu.Unlock()

	if len(a.units) > 0 {
		// is-active exits non-zero when any unit isn't active; its output
		// still has one line per unit.
		out, err := command(ctx, 10*time.Second, "systemctl", append([]string{"is-active"}, a.units...)...)
		if out == "" && err != nil {
			errf("systemctl is-active", err)
		} else {
			s.Units = ParseIsActive(a.units, out)
		}
	}
	if out, err := command(ctx, 10*time.Second, "systemctl", "list-units", "--state=failed", "--no-legend", "--plain", "--no-pager"); err != nil {
		errf("systemctl list-units", err)
	} else {
		s.FailedUnits = ParseFailedUnits(out)
	}

	for _, p := range a.cfg.Certs {
		s.Certs = append(s.Certs, ReadCert(p))
	}

	if a.has("mysql") {
		if a.db == nil {
			s.MySQL = &fleet.MySQLStatus{Err: "no monitoring user configured", Replicas: -1}
		} else {
			s.MySQL = collectMySQL(ctx, a.db)
		}
	}
	if a.has("redis") && a.rdb != nil {
		s.Redis = a.collectRedis(ctx)
	}
	return s
}

func (a *Agent) collectRedis(ctx context.Context) *fleet.RedisStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := a.rdb.Info(ctx, "server", "memory", "clients", "stats", "keyspace").Result()
	if err != nil {
		return &fleet.RedisStatus{Err: short(err.Error())}
	}
	return ParseRedisInfo(info)
}

// updatesLoop asks apt for pending updates now and then every hour: it
// reads the package lists (a few seconds of CPU), never changes them.
func (a *Agent) updatesLoop(ctx context.Context) {
	const aptCheck = "/usr/lib/update-notifier/apt-check"
	if _, err := os.Stat(aptCheck); err != nil {
		return // not Ubuntu's update-notifier: nothing to report
	}
	for {
		// apt-check prints "<updates>;<security>" on stderr.
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		cmd := exec.CommandContext(cctx, aptCheck)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		cmd.Stdout = io.Discard
		err := cmd.Run()
		cancel()
		a.updMu.Lock()
		if err != nil {
			a.updErr = short(err.Error())
		} else if total, sec, perr := ParseAptCheck(stderr.String()); perr != nil {
			a.updErr = short(perr.Error())
		} else {
			a.updates = &fleet.Updates{Total: total, Security: sec, Checked: time.Now().UTC()}
			a.updErr = ""
		}
		a.updMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// command runs name with a deadline and returns its standard output.
func command(ctx context.Context, d time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// ReadCert reads the first certificate of a PEM file: only its expiry and
// subject go in the snapshot.
func ReadCert(path string) fleet.Cert {
	c := fleet.Cert{Name: filepath.Base(path)}
	f, err := os.Open(path)
	if err != nil {
		c.Err = short(err.Error())
		return c
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		c.Err = short(err.Error())
		return c
	}
	cert, err := fleet.FirstCert(b)
	if err != nil {
		c.Err = err.Error()
		return c
	}
	c.Subject = cert.Subject.CommonName
	c.NotAfter = cert.NotAfter.UTC()
	return c
}

// short keeps an error line to one line of reasonable length.
func short(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
