// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// State is how a check judges what it looked at.
type State string

const (
	Good    State = "good"
	Warn    State = "warn"
	Bad     State = "bad"
	Unknown State = "unknown" // not enough to judge (a stale report's details)
)

func (s State) rank() int {
	switch s {
	case Bad:
		return 3
	case Warn:
		return 2
	case Good:
		return 1
	}
	return 0
}

func worst(a, b State) State {
	if b.rank() > a.rank() {
		return b
	}
	return a
}

// Thresholds. The tab and the alerts read the same ones (Evaluate).
const (
	DiskWarnPct     = 85.0
	DiskBadPct      = 95.0
	MemWarnAvailPct = 10.0 // memory available below this share of the total
	MemBadAvailPct  = 5.0
	SwapWarnPct     = 50.0
	CPUWarnPct      = 90.0
	LoadWarnPerCPU  = 2.0
	LagWarn         = 30  // replication, seconds behind
	LagBad          = 300 //
	CertWarnDays    = 14
	CertBadDays     = 7
	ClockWarnMs     = 100.0
	ClockBadMs      = 1000.0
	FDWarnPct       = 80.0
	ConnsWarnPct    = 80.0 // MySQL threads connected / max_connections
	RedisMemWarnPct = 90.0
	ErrorsWarn15m   = 300 // bridge ERROR lines in 15 minutes
)

// staleAfter is when a report counts as missed: three intervals, at least
// 90 s.
func staleAfter(intervalS int) time.Duration {
	d := 3 * time.Duration(intervalS) * time.Second
	return max(d, 90*time.Second)
}

// Check is one line of a host's card - and, unless Quiet, something the
// alerts mail about when it turns warn or bad.
type Check struct {
	ID     string `json:"id"`   // stable: report, disk:/, replication...
	Name   string `json:"name"` // shown: "Disk /"
	State  State  `json:"state"`
	Detail string `json:"detail"`
	// Short is the few words an alert's subject uses ("disk / 96%").
	Short string `json:"short,omitempty"`
	// Quiet checks show in the tab but are never mailed: pending
	// updates, a reboot to do, a busy CPU.
	Quiet bool `json:"quiet,omitempty"`
}

// HostView is one server as the tab shows it.
type HostView struct {
	Name string `json:"name"`
	// HostSeen / BridgeSeen: when the agent / the bridge process last
	// reported (zero: never).
	HostSeen   time.Time       `json:"host_seen,omitzero"`
	BridgeSeen time.Time       `json:"bridge_seen,omitzero"`
	Host       *HostSnapshot   `json:"host,omitempty"`
	Bridge     *BridgeSnapshot `json:"bridge,omitempty"`
	// Stale: the agent's last report is too old (or gone), so the
	// figures shown are old ones, or none.
	Stale  bool     `json:"stale"`
	Roles  []string `json:"roles"`
	Checks []Check  `json:"checks"`
	State  State    `json:"state"` // the worst check
}

// Evaluate fills h's Roles, Checks and State from its snapshots as of
// now. The tab and the alerts both call it.
func Evaluate(h *HostView, now time.Time) {
	h.Checks = h.Checks[:0]
	add := func(c Check) { h.Checks = append(h.Checks, c) }
	hs := h.Host

	// The agent's report itself.
	switch {
	case hs == nil && h.HostSeen.IsZero():
		h.Stale = true
		add(Check{ID: "report", Name: "Agent report", State: Bad, Detail: "otc-fleet-agent has never reported from this host", Short: "no agent report"})
	case hs == nil:
		h.Stale = true
		add(Check{ID: "report", Name: "Agent report", State: Bad, Detail: "no report since " + stamp(h.HostSeen) + " (" + ago(now.Sub(h.HostSeen)) + ")", Short: "stopped reporting"})
	default:
		age := now.Sub(hs.Time)
		if age > staleAfter(hs.Interval) {
			h.Stale = true
			add(Check{ID: "report", Name: "Agent report", State: Bad, Detail: "last report " + ago(age) + ", expected every " + fmt.Sprint(hs.Interval) + " s", Short: "stopped reporting"})
		} else {
			add(Check{ID: "report", Name: "Agent report", State: Good, Detail: "every " + fmt.Sprint(hs.Interval) + " s, last " + ago(age) + " · agent " + hs.Version})
		}
	}

	expect := func(role string) bool { return hs != nil && slices.Contains(hs.Expect, role) }
	isBridge := h.Bridge != nil || !h.BridgeSeen.IsZero() || expect("bridge")

	// Roles, as chips.
	h.Roles = []string{}
	if isBridge {
		h.Roles = append(h.Roles, "bridge")
	}
	if hs != nil && hs.MySQL != nil && hs.MySQL.Role != "" {
		h.Roles = append(h.Roles, "mysql "+hs.MySQL.Role)
	} else if expect("mysql") {
		h.Roles = append(h.Roles, "mysql")
	}
	if expect("redis") || (hs != nil && hs.Redis != nil) {
		h.Roles = append(h.Roles, "redis")
	}
	if expect("certbot") {
		h.Roles = append(h.Roles, "cert renewal")
	}

	if isBridge {
		evalBridge(h, now, add)
	}
	if hs != nil {
		evalHost(h, hs, now, add)
	}

	h.State = Good
	for _, c := range h.Checks {
		h.State = worst(h.State, c.State)
	}
}

func evalBridge(h *HostView, now time.Time, add func(Check)) {
	b := h.Bridge
	stale := b == nil || now.Sub(b.Time) > staleAfter(b.Interval)
	if stale {
		d := "the bridge process has never reported"
		if b != nil {
			d = "the bridge process last reported " + ago(now.Sub(b.Time))
		} else if !h.BridgeSeen.IsZero() {
			d = "the bridge process last reported " + ago(now.Sub(h.BridgeSeen))
		}
		add(Check{ID: "bridge", Name: "Bridge", State: Bad, Detail: d, Short: "bridge not reporting"})
		// What it said last is no longer known.
		for _, id := range []string{"bridge-cert", "bridge-fds", "bridge-errors"} {
			add(Check{ID: id, Name: bridgeCheckName[id], State: Unknown, Detail: "no current report"})
		}
		return
	}
	add(Check{ID: "bridge", Name: "Bridge", State: Good, Detail: fmt.Sprintf("%s, up %s · %d devices (%d connections, %d idle) · %d clients",
		b.Version, dur(now.Sub(b.Started)), b.Devices, b.DeviceConns, b.Idle, b.Clients)})

	if b.CertErr != "" {
		add(Check{ID: "bridge-cert", Name: bridgeCheckName["bridge-cert"], State: Bad, Detail: b.CertErr, Short: "served certificate unreadable"})
	} else if !b.CertNotAfter.IsZero() {
		add(certCheck("bridge-cert", bridgeCheckName["bridge-cert"], b.CertNotAfter, now))
	}

	if b.FDs >= 0 && b.MaxFDs > 0 {
		pct := pctOf(uint64(b.FDs), uint64(b.MaxFDs))
		c := Check{ID: "bridge-fds", Name: bridgeCheckName["bridge-fds"], State: Good,
			Detail: fmt.Sprintf("%d of %d (%.0f%%) · %d goroutines", b.FDs, b.MaxFDs, pct, b.Goroutines)}
		if pct >= FDWarnPct {
			c.State, c.Short = Warn, fmt.Sprintf("bridge open files %.0f%%", pct)
		}
		add(c)
	}

	c := Check{ID: "bridge-errors", Name: bridgeCheckName["bridge-errors"], State: Good, Quiet: true,
		Detail: fmt.Sprintf("%d error lines in the last 15 minutes", b.Errors15m)}
	if b.Errors15m >= ErrorsWarn15m {
		c.State, c.Short = Warn, fmt.Sprintf("%d bridge errors in 15 min", b.Errors15m)
	}
	add(c)
}

var bridgeCheckName = map[string]string{
	"bridge-cert":   "Served certificate",
	"bridge-fds":    "Bridge open files",
	"bridge-errors": "Bridge log errors",
}

func evalHost(h *HostView, s *HostSnapshot, now time.Time, add func(Check)) {
	if h.Stale {
		// The figures are old: shown, not judged.
		return
	}

	// Services the roles need, and any unit systemd marks failed.
	if len(s.Units) > 0 {
		var down []string
		names := make([]string, 0, len(s.Units))
		for u := range s.Units {
			names = append(names, u)
		}
		sort.Strings(names)
		for _, u := range names {
			if st := s.Units[u]; st != "active" {
				down = append(down, u+" "+st)
			}
		}
		if len(down) > 0 {
			add(Check{ID: "services", Name: "Services", State: Bad, Detail: strings.Join(down, ", "), Short: strings.Join(down, ", ")})
		} else {
			add(Check{ID: "services", Name: "Services", State: Good, Detail: strings.Join(names, ", ") + " active"})
		}
	}
	if len(s.FailedUnits) > 0 {
		add(Check{ID: "failed-units", Name: "Failed units", State: Bad, Detail: strings.Join(s.FailedUnits, ", "), Short: "failed: " + strings.Join(s.FailedUnits, ", ")})
	} else {
		add(Check{ID: "failed-units", Name: "Failed units", State: Good, Detail: "none"})
	}

	if s.CPU != nil {
		c := Check{ID: "cpu", Name: "CPU", State: Good, Quiet: true,
			Detail: fmt.Sprintf("%.0f%% busy, %.0f%% iowait, %.0f%% steal · %d CPUs", s.CPU.Busy, s.CPU.IOWait, s.CPU.Steal, s.CPUs)}
		if s.CPU.Busy >= CPUWarnPct {
			c.State, c.Short = Warn, fmt.Sprintf("CPU %.0f%%", s.CPU.Busy)
		}
		add(c)
	}
	if s.CPUs > 0 {
		c := Check{ID: "load", Name: "Load", State: Good, Quiet: true,
			Detail: fmt.Sprintf("%.2f %.2f %.2f on %d CPUs", s.Load[0], s.Load[1], s.Load[2], s.CPUs)}
		if s.Load[1] >= LoadWarnPerCPU*float64(s.CPUs) {
			c.State, c.Short = Warn, fmt.Sprintf("load %.1f", s.Load[1])
		}
		add(c)
	}

	if m := s.Memory; m != nil {
		avail := pctOf(m.Available, m.Total)
		c := Check{ID: "memory", Name: "Memory", State: Good, Detail: fmt.Sprintf("%s available of %s (%.0f%%)", bytesText(m.Available), bytesText(m.Total), avail)}
		switch {
		case avail < MemBadAvailPct:
			c.State = Bad
		case avail < MemWarnAvailPct:
			c.State = Warn
		}
		if c.State != Good {
			c.Short = fmt.Sprintf("memory %.0f%% available", avail)
		}
		add(c)
		if m.SwapTotal > 0 {
			used := pctOf(m.SwapTotal-min(m.SwapFree, m.SwapTotal), m.SwapTotal)
			c := Check{ID: "swap", Name: "Swap", State: Good, Detail: fmt.Sprintf("%.0f%% of %s used", used, bytesText(m.SwapTotal))}
			if used >= SwapWarnPct {
				c.State, c.Short = Warn, fmt.Sprintf("swap %.0f%%", used)
			}
			add(c)
		}
	}

	for _, d := range s.Disks {
		used := pctOf(d.Used, d.Used+d.Avail) // as df: reserved blocks don't count
		st := levelPct(used, DiskWarnPct, DiskBadPct)
		detail := fmt.Sprintf("%.0f%% used, %s free of %s", used, bytesText(d.Avail), bytesText(d.Total))
		short := fmt.Sprintf("disk %s %.0f%%", d.Mount, used)
		if d.Inodes > 0 {
			iu := pctOf(d.Inodes-min(d.InodesFree, d.Inodes), d.Inodes)
			detail += fmt.Sprintf(" · inodes %.0f%%", iu)
			if is := levelPct(iu, DiskWarnPct, DiskBadPct); is.rank() > st.rank() {
				st, short = is, fmt.Sprintf("disk %s inodes %.0f%%", d.Mount, iu)
			}
		}
		c := Check{ID: "disk:" + d.Mount, Name: "Disk " + d.Mount, State: st, Detail: detail}
		if st != Good {
			c.Short = short
		}
		add(c)
	}

	add(raidCheck(s.RAID))

	if c := s.Clock; c != nil {
		off := math.Abs(c.OffsetMs)
		ch := Check{ID: "clock", Name: "Clock", State: Good, Detail: fmt.Sprintf("synchronised, offset %.1f ms", c.OffsetMs)}
		switch {
		case off >= ClockBadMs:
			ch.State, ch.Short = Bad, fmt.Sprintf("clock off by %.0f ms", c.OffsetMs)
		case !c.Synced:
			ch.State, ch.Short, ch.Detail = Warn, "clock not synchronised", fmt.Sprintf("not synchronised (NTP), offset %.1f ms", c.OffsetMs)
		case off >= ClockWarnMs:
			ch.State, ch.Short = Warn, fmt.Sprintf("clock off by %.0f ms", c.OffsetMs)
		}
		ch.Detail += " · " + boot(s, now)
		add(ch)
	}

	reb := Check{ID: "reboot", Name: "Reboot", State: Good, Quiet: true, Detail: "not required"}
	if s.RebootRequired {
		reb.State, reb.Short = Warn, "reboot required"
		reb.Detail = "required"
		if s.RebootPackages > 0 {
			reb.Detail += fmt.Sprintf(" by %d updated packages", s.RebootPackages)
		}
	}
	add(reb)
	if u := s.Updates; u != nil {
		c := Check{ID: "updates", Name: "Updates", State: Good, Quiet: true,
			Detail: fmt.Sprintf("%d pending, %d security · checked %s", u.Total, u.Security, ago(now.Sub(u.Checked)))}
		if u.Security > 0 {
			c.State, c.Short = Warn, fmt.Sprintf("%d security updates", u.Security)
		}
		add(c)
	}

	for _, ct := range s.Certs {
		id, name := "cert:"+ct.Name, "Certificate "+ct.Name
		if ct.Err != "" {
			add(Check{ID: id, Name: name, State: Warn, Detail: "can't read it: " + ct.Err, Short: "certificate " + ct.Name + " unreadable"})
			continue
		}
		add(certCheck(id, name, ct.NotAfter, now))
	}

	if s.MySQL != nil || slices.Contains(s.Expect, "mysql") {
		evalMySQL(s.MySQL, add)
	}
	if s.Redis != nil || slices.Contains(s.Expect, "redis") {
		evalRedis(s.Redis, add)
	}
}

func boot(s *HostSnapshot, now time.Time) string {
	if s.BootTime.IsZero() {
		return "up " + dur(time.Duration(s.Uptime)*time.Second)
	}
	return "booted " + stamp(s.BootTime) + " (up " + dur(time.Duration(s.Uptime)*time.Second) + ")"
}

func raidCheck(r *RAID) Check {
	c := Check{ID: "raid", Name: "RAID", State: Good}
	if r == nil || !r.Present {
		c.Detail = "no RAID"
		return c
	}
	var parts []string
	for _, a := range r.Arrays {
		st, txt := Good, a.Name+" "+a.Level+" "+a.Status
		switch {
		case !a.Active:
			st, txt = Bad, a.Name+" inactive"
		case a.Failed > 0 || (a.Want > 0 && a.Have < a.Want) || strings.Contains(a.Status, "_"):
			st, txt = Bad, fmt.Sprintf("%s degraded %s", a.Name, a.Status)
			if a.Failed > 0 {
				txt += fmt.Sprintf(", %d failed", a.Failed)
			}
		}
		if a.Sync != "" {
			txt += " (" + a.Sync + ")"
			st = worst(st, Warn)
		}
		c.State = worst(c.State, st)
		parts = append(parts, txt)
	}
	c.Detail = strings.Join(parts, " · ")
	if c.State != Good {
		var bad []string
		for _, a := range r.Arrays {
			if !a.Active || a.Failed > 0 || strings.Contains(a.Status, "_") || a.Sync != "" {
				bad = append(bad, a.Name)
			}
		}
		c.Short = "RAID " + strings.Join(bad, ",")
		if c.State == Bad {
			c.Short += " degraded"
		} else {
			c.Short += " syncing"
		}
	}
	return c
}

func evalMySQL(m *MySQLStatus, add func(Check)) {
	if m == nil || !m.Up {
		d := "not answering"
		if m != nil && m.Err != "" {
			d = m.Err
		}
		add(Check{ID: "mysql", Name: "MySQL", State: Bad, Detail: d, Short: "MySQL down"})
		return
	}
	c := Check{ID: "mysql", Name: "MySQL", State: Good,
		Detail: fmt.Sprintf("%s, up %s · %d connections (peak %d of %d), %d running", m.Version, dur(time.Duration(m.Uptime)*time.Second), m.Connected, m.PeakConns, m.MaxConns, m.Running)}
	if m.Role == "replica" && !m.SuperReadOnly && !m.ReadOnly {
		c.State, c.Short = Warn, "MySQL replica writable"
		c.Detail += " · the replica is not read-only"
	}
	if m.MaxConns > 0 {
		if p := pctOf(uint64(m.Connected), uint64(m.MaxConns)); p >= ConnsWarnPct {
			c.State, c.Short = Warn, fmt.Sprintf("MySQL connections %.0f%%", p)
		}
	}
	if m.Err != "" {
		c.State = worst(c.State, Warn)
		c.Detail += " · " + m.Err
		if c.Short == "" {
			c.Short = "MySQL monitoring incomplete"
		}
	}
	add(c)

	switch {
	case m.Role == "replica" && m.Replica != nil:
		add(replicationCheck(m.Replica))
	case m.Role == "primary":
		c := Check{ID: "replication", Name: "Replication", State: Good}
		switch {
		case m.Replicas < 0:
			c.Detail = "primary · replicas can't be counted (no PROCESS grant)"
		case m.Replicas == 0:
			c.State, c.Short, c.Detail = Warn, "no replica connected", "primary · no replica is streaming its binary log"
		default:
			c.Detail = fmt.Sprintf("primary · %d replica(s) streaming", m.Replicas)
		}
		add(c)
	}
}

func replicationCheck(r *ReplicaStatus) Check {
	c := Check{ID: "replication", Name: "Replication", State: Good}
	parts := []string{"IO " + r.IORunning, "SQL " + r.SQLRunning}
	if r.Behind != nil {
		parts = append(parts, fmt.Sprintf("%d s behind", *r.Behind))
	} else {
		parts = append(parts, "lag unknown")
	}
	if r.Backlog >= 0 {
		parts = append(parts, fmt.Sprintf("%d transactions to apply", r.Backlog))
	}
	if r.Source != "" {
		parts = append(parts, "from "+r.Source)
	}
	switch {
	case r.IORunning != "Yes" || r.SQLRunning != "Yes":
		c.State = Bad
		stopped := []string{}
		if r.IORunning != "Yes" {
			stopped = append(stopped, "IO "+strings.ToLower(r.IORunning))
		}
		if r.SQLRunning != "Yes" {
			stopped = append(stopped, "SQL "+strings.ToLower(r.SQLRunning))
		}
		c.Short = "replication stopped (" + strings.Join(stopped, ", ") + ")"
	case r.Behind == nil:
		c.State, c.Short = Warn, "replication lag unknown"
	case *r.Behind >= LagBad:
		c.State, c.Short = Bad, fmt.Sprintf("replication %d s behind", *r.Behind)
	case *r.Behind >= LagWarn:
		c.State, c.Short = Warn, fmt.Sprintf("replication %d s behind", *r.Behind)
	}
	if r.IOErrno != 0 {
		parts = append(parts, fmt.Sprintf("last IO error %d: %s", r.IOErrno, r.IOError))
	}
	if r.SQLErrno != 0 {
		parts = append(parts, fmt.Sprintf("last SQL error %d: %s", r.SQLErrno, r.SQLError))
	}
	c.Detail = "replica · " + strings.Join(parts, " · ")
	return c
}

func evalRedis(r *RedisStatus, add func(Check)) {
	if r == nil || !r.Up {
		d := "not answering"
		if r != nil && r.Err != "" {
			d = r.Err
		}
		add(Check{ID: "redis", Name: "Redis", State: Bad, Detail: d, Short: "Redis down"})
		return
	}
	c := Check{ID: "redis", Name: "Redis", State: Good,
		Detail: fmt.Sprintf("%s, up %s · %s used · %d clients · %d keys", r.Version, dur(time.Duration(r.Uptime)*time.Second), bytesText(uint64(r.Used)), r.Clients, r.Keys)}
	if r.MaxMemory > 0 {
		p := pctOf(uint64(r.Used), uint64(r.MaxMemory))
		c.Detail += fmt.Sprintf(" (%.0f%% of maxmemory)", p)
		if p >= RedisMemWarnPct {
			c.State, c.Short = Warn, fmt.Sprintf("Redis memory %.0f%%", p)
		}
	}
	if r.Evicted > 0 {
		c.Detail += fmt.Sprintf(" · %d keys evicted", r.Evicted)
	}
	add(c)
}

func certCheck(id, name string, notAfter, now time.Time) Check {
	days := int(math.Floor(notAfter.Sub(now).Hours() / 24))
	c := Check{ID: id, Name: name, State: Good, Detail: fmt.Sprintf("expires %s (%d days)", notAfter.UTC().Format("2006-01-02"), days)}
	switch {
	case !now.Before(notAfter):
		c.State, c.Short, c.Detail = Bad, strings.ToLower(name)+" expired", "expired "+notAfter.UTC().Format("2006-01-02")
	case days < CertBadDays:
		c.State, c.Short = Bad, fmt.Sprintf("%s expires in %d days", strings.ToLower(name), days)
	case days < CertWarnDays:
		c.State, c.Short = Warn, fmt.Sprintf("%s expires in %d days", strings.ToLower(name), days)
	}
	return c
}

func levelPct(p, warn, bad float64) State {
	switch {
	case p >= bad:
		return Bad
	case p >= warn:
		return Warn
	}
	return Good
}

func pctOf(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func bytesText(n uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func ago(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return dur(d) + " ago"
}

func dur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1f h", d.Hours())
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// FirstCert is the first CERTIFICATE block of a PEM text; any other block
// (a private key kept in the same file) is skipped, never parsed.
func FirstCert(b []byte) (*x509.Certificate, error) {
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return nil, errors.New("no certificate in the file")
		}
		if blk.Type == "CERTIFICATE" {
			return x509.ParseCertificate(blk.Bytes)
		}
	}
}
