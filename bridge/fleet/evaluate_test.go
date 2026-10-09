// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// healthy is a fresh report from a host with nothing wrong.
func healthy(name string, roles ...string) *HostSnapshot {
	return &HostSnapshot{
		Name: name, Time: t0.Add(-10 * time.Second), Interval: 30, Version: "abc", Expect: roles,
		CPUs: 8, Load: [3]float64{0.5, 0.4, 0.3},
		CPU:         &CPUUsage{Busy: 5},
		Memory:      &Memory{Total: 64 << 30, Available: 40 << 30, SwapTotal: 4 << 30, SwapFree: 4 << 30},
		Disks:       []Disk{{Mount: "/", FS: "ext4", Total: 1000, Used: 500, Avail: 500, Inodes: 100, InodesFree: 90}},
		RAID:        &RAID{Present: true, Arrays: []MDArray{{Name: "md2", Level: "raid1", Active: true, Devices: 2, Want: 2, Have: 2, Status: "[UU]"}}},
		Units:       map[string]string{"wg-quick@wg0": "active", "ufw": "active"},
		FailedUnits: []string{},
		Clock:       &Clock{Synced: true, OffsetMs: 0.3},
	}
}

func check(t *testing.T, h *HostView, id string) Check {
	t.Helper()
	for _, c := range h.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", id, h.Checks)
	return Check{}
}

func eval(hs *HostSnapshot, bs *BridgeSnapshot) *HostView {
	h := &HostView{Name: hs.Name, Host: hs, Bridge: bs, HostSeen: hs.Time}
	if bs != nil {
		h.BridgeSeen = bs.Time
	}
	Evaluate(h, t0)
	return h
}

func TestHealthyHostIsGood(t *testing.T) {
	h := eval(healthy("redis", "redis"), nil)
	h.Host.Redis = &RedisStatus{Up: true}
	Evaluate(h, t0)
	if h.State != Good || h.Stale {
		t.Fatalf("%s %+v", h.State, h.Checks)
	}
	if got := strings.Join(h.Roles, ","); got != "redis" {
		t.Fatal(got)
	}
}

func TestDiskThresholds(t *testing.T) {
	for _, c := range []struct {
		used, avail uint64
		want        State
	}{{840, 160, Good}, {850, 150, Warn}, {949, 51, Warn}, {950, 50, Bad}} {
		hs := healthy("bridge1")
		hs.Disks[0].Used, hs.Disks[0].Avail = c.used, c.avail
		if got := check(t, eval(hs, nil), "disk:/"); got.State != c.want {
			t.Errorf("%d%%: %s", c.used/10, got.State)
		}
	}
	// Inodes count too.
	hs := healthy("bridge1")
	hs.Disks[0].InodesFree = 3
	if got := check(t, eval(hs, nil), "disk:/"); got.State != Bad || !strings.Contains(got.Short, "inodes") {
		t.Fatalf("%+v", got)
	}
}

func TestStaleAndMissingReports(t *testing.T) {
	hs := healthy("bridge2")
	hs.Disks[0].Used, hs.Disks[0].Avail = 990, 10 // would be bad, but it's old
	hs.Time = t0.Add(-91 * time.Second)
	h := eval(hs, nil)
	if !h.Stale || check(t, h, "report").State != Bad {
		t.Fatalf("%+v", h.Checks)
	}
	for _, c := range h.Checks {
		if c.ID != "report" {
			t.Fatalf("a stale host's old figures are not judged: %+v", c)
		}
	}
	// The snapshot expired: only when it was last seen.
	h = &HostView{Name: "bridge2", HostSeen: t0.Add(-time.Hour)}
	Evaluate(h, t0)
	if c := check(t, h, "report"); c.State != Bad || !strings.Contains(c.Detail, "1.0 h ago") {
		t.Fatalf("%+v", c)
	}
}

func TestBridgeProcessChecks(t *testing.T) {
	bs := &BridgeSnapshot{Node: "bridge1", Time: t0.Add(-5 * time.Second), Interval: 30, Version: "abc", Started: t0.Add(-time.Hour),
		Devices: 3, DeviceConns: 15, Idle: 12, Clients: 4, FDs: 100, MaxFDs: 1000, CertNotAfter: t0.Add(60 * 24 * time.Hour)}
	h := eval(healthy("bridge1", "bridge"), bs)
	if h.State != Good || check(t, h, "bridge").State != Good || h.Roles[0] != "bridge" {
		t.Fatalf("%+v", h.Checks)
	}
	if d := check(t, h, "bridge").Detail; !strings.Contains(d, "3 devices") || !strings.Contains(d, "4 clients") {
		t.Fatal(d)
	}
	// The process stopped reporting: bad, and what it said last unknown.
	bs.Time = t0.Add(-5 * time.Minute)
	h = eval(healthy("bridge1", "bridge"), bs)
	if check(t, h, "bridge").State != Bad || check(t, h, "bridge-cert").State != Unknown {
		t.Fatalf("%+v", h.Checks)
	}
	// Expected, never seen.
	h = eval(healthy("bridge1", "bridge"), nil)
	if check(t, h, "bridge").State != Bad {
		t.Fatalf("%+v", h.Checks)
	}
}

func TestCertThresholds(t *testing.T) {
	for days, want := range map[int]State{30: Good, 14: Good, 13: Warn, 7: Warn, 6: Bad, -1: Bad} {
		c := certCheck("cert:x", "Certificate x", t0.Add(time.Duration(days)*24*time.Hour+time.Hour), t0)
		if c.State != want {
			t.Errorf("%d days: %s (%s)", days, c.State, c.Detail)
		}
	}
}

func TestReplicationStates(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		r    ReplicaStatus
		want State
	}{
		{ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", Behind: n(0), Backlog: 0}, Good},
		{ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", Behind: n(29)}, Good},
		{ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", Behind: n(30)}, Warn},
		{ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", Behind: n(300)}, Bad},
		{ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes"}, Warn}, // NULL lag
		{ReplicaStatus{IORunning: "Connecting", SQLRunning: "Yes", IOErrno: 2003}, Bad},
		{ReplicaStatus{IORunning: "Yes", SQLRunning: "No", SQLErrno: 1062}, Bad},
	} {
		got := replicationCheck(&c.r)
		if got.State != c.want {
			t.Errorf("%+v: %s (%s)", c.r, got.State, got.Detail)
		}
	}
	if s := replicationCheck(&ReplicaStatus{IORunning: "Connecting", SQLRunning: "Yes"}).Short; s != "replication stopped (IO connecting)" {
		t.Fatal(s)
	}
}

func TestMySQLRoles(t *testing.T) {
	// Expected, not answering.
	hs := healthy("bridge1", "bridge", "mysql")
	hs.MySQL = &MySQLStatus{Err: "connect: error 1045: Access denied for user '…'", Replicas: -1}
	h := eval(hs, nil)
	if check(t, h, "mysql").State != Bad {
		t.Fatalf("%+v", h.Checks)
	}
	// A primary with its replica streaming.
	hs.MySQL = &MySQLStatus{Up: true, Role: "primary", MaxConns: 100, Connected: 10, Replicas: 1}
	h = eval(hs, nil)
	if check(t, h, "mysql").State != Good || check(t, h, "replication").State != Good || !strings.Contains(strings.Join(h.Roles, ","), "mysql primary") {
		t.Fatalf("%+v %v", h.Checks, h.Roles)
	}
	// ... and without it.
	hs.MySQL.Replicas = 0
	if c := check(t, eval(hs, nil), "replication"); c.State != Warn {
		t.Fatalf("%+v", c)
	}
	// A replica that accepts writes.
	hs.MySQL = &MySQLStatus{Up: true, Role: "replica", MaxConns: 100, Replicas: 0, Replica: &ReplicaStatus{IORunning: "Yes", SQLRunning: "Yes", Behind: new(int64)}}
	if c := check(t, eval(hs, nil), "mysql"); c.State != Warn {
		t.Fatalf("%+v", c)
	}
}

func TestRAIDAndServices(t *testing.T) {
	hs := healthy("bridge1")
	hs.RAID = &RAID{}
	if c := check(t, eval(hs, nil), "raid"); c.State != Good || c.Detail != "no RAID" {
		t.Fatalf("%+v", c)
	}
	hs.RAID = &RAID{Present: true, Arrays: []MDArray{{Name: "md1", Active: true, Want: 2, Have: 1, Status: "[U_]", Failed: 1, Sync: "recovery 8.9%"}}}
	if c := check(t, eval(hs, nil), "raid"); c.State != Bad || c.Short != "RAID md1 degraded" {
		t.Fatalf("%+v", c)
	}
	hs.RAID.Arrays[0] = MDArray{Name: "md0", Active: true, Want: 2, Have: 2, Status: "[UU]", Sync: "check 50%"}
	if c := check(t, eval(hs, nil), "raid"); c.State != Warn {
		t.Fatalf("%+v", c)
	}

	hs = healthy("bridge1")
	hs.Units["otc_bridge"] = "failed"
	hs.FailedUnits = []string{"otc_bridge.service"}
	h := eval(hs, nil)
	if check(t, h, "services").State != Bad || check(t, h, "failed-units").State != Bad {
		t.Fatalf("%+v", h.Checks)
	}
}

func TestMemoryClockAndQuietChecks(t *testing.T) {
	hs := healthy("bridge1")
	hs.Memory.Available = hs.Memory.Total / 25 // 4%
	hs.Clock = &Clock{Synced: false}
	hs.RebootRequired = true
	hs.Updates = &Updates{Total: 4, Security: 2, Checked: t0}
	hs.CPU.Busy = 95
	h := eval(hs, nil)
	if check(t, h, "memory").State != Bad || check(t, h, "clock").State != Warn {
		t.Fatalf("%+v", h.Checks)
	}
	for _, id := range []string{"reboot", "updates", "cpu"} {
		if c := check(t, h, id); c.State != Warn || !c.Quiet {
			t.Errorf("%s: %+v", id, c)
		}
	}
	hs.Clock = &Clock{Synced: true, OffsetMs: -1500}
	if c := check(t, eval(hs, nil), "clock"); c.State != Bad {
		t.Fatalf("%+v", c)
	}
}
