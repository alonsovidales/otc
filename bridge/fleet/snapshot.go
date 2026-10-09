// SPDX-License-Identifier: AGPL-3.0-or-later

// Package fleet is the admin panel's Fleet tab and its email alerts: what
// each server of the bridge cluster (bridge/cluster/README.md) reports
// about itself, and how that is judged.
//
// Two writers, one reader:
//   - otc-fleet-agent (bridge/fleetagent, package fleet/agent) runs on
//     every host - the redis one included, which has no bridge - and every
//     30 s writes a HostSnapshot (CPU, memory, disks, RAID, units, MySQL,
//     Redis...) to Redis as fleet:host:<name>;
//   - every bridge node writes a BridgeSnapshot (devices and clients
//     connected, its version, goroutines...) as fleet:bridge:<node>.
//
// Both keys expire (cSnapshotTTL), and fleet:known-hosts /
// fleet:known-bridges remember when each one last reported, so a host that
// stops reporting shows as stale, then as missing, never as gone. The
// bridge reads them all back (Read) for GET /admin/api/fleet, and Evaluate
// turns a host into the good/warn/bad checks the tab shows and the alerts
// (alerts.go) mail - one function, so the two never disagree.
//
// Nothing personal goes in: counts only - no domains, addresses, emails,
// and MySQL errors with their quoted values taken out (redactQuoted).
package fleet

import "time"

// Version is the build's commit, set by the makefile (-ldflags -X).
var Version = "dev"

const (
	// KeyHostPrefix + name is a host's snapshot; KeyBridgePrefix + node a
	// bridge node's. KeyKnownHosts / KeyKnownBridges: name -> the unix
	// time it last reported (no TTL: a host that stopped is still listed).
	KeyHostPrefix   = "fleet:host:"
	KeyBridgePrefix = "fleet:bridge:"
	KeyKnownHosts   = "fleet:known-hosts"
	KeyKnownBridges = "fleet:known-bridges"

	// SnapshotTTL is how long a snapshot outlives its writer. After it,
	// the card shows only "no report since ...".
	SnapshotTTL = 10 * time.Minute

	// DefaultInterval is how often agents and bridges report.
	DefaultInterval = 30 * time.Second

	// MaxSnapshotBytes bounds what Read accepts for one snapshot.
	MaxSnapshotBytes = 256 << 10
)

// HostSnapshot is what otc-fleet-agent reports about its host.
type HostSnapshot struct {
	Name     string    `json:"name"`
	Time     time.Time `json:"time"`
	Interval int       `json:"interval_s"` // seconds between reports
	Version  string    `json:"version"`
	// Expect is the roles the host is configured for (-roles): bridge,
	// mysql, redis, certbot. A role expected but not running is bad.
	Expect []string `json:"expect"`

	OS       string    `json:"os"`
	Kernel   string    `json:"kernel"`
	BootTime time.Time `json:"boot_time"`
	Uptime   int64     `json:"uptime_s"`

	CPUs   int        `json:"cpus"`
	Load   [3]float64 `json:"load"`
	CPU    *CPUUsage  `json:"cpu,omitempty"`
	Memory *Memory    `json:"memory,omitempty"`
	Disks  []Disk     `json:"disks"`
	RAID   *RAID      `json:"raid,omitempty"`
	Net    []NetIf    `json:"net"`

	// Units is the active state of the services this host's roles need
	// (otc_bridge, mysql, redis-server, wg-quick@wg0...).
	Units       map[string]string `json:"units"`
	FailedUnits []string          `json:"failed_units"`

	RebootRequired bool     `json:"reboot_required"`
	RebootPackages int      `json:"reboot_packages"`
	Updates        *Updates `json:"updates,omitempty"`
	Clock          *Clock   `json:"clock,omitempty"`
	Certs          []Cert   `json:"certs"`

	MySQL *MySQLStatus `json:"mysql,omitempty"`
	Redis *RedisStatus `json:"redis,omitempty"`

	// Errors are what the agent could not collect, one line each.
	Errors []string `json:"errors"`
}

// CPUUsage is the share of CPU time over the last interval, in percent.
type CPUUsage struct {
	Busy   float64 `json:"busy"`
	IOWait float64 `json:"iowait"`
	Steal  float64 `json:"steal"`
}

// Memory is from /proc/meminfo, in bytes.
type Memory struct {
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
	SwapTotal uint64 `json:"swap_total"`
	SwapFree  uint64 `json:"swap_free"`
}

// Disk is one mounted filesystem.
type Disk struct {
	Mount      string `json:"mount"`
	FS         string `json:"fs"`
	Total      uint64 `json:"total"`
	Used       uint64 `json:"used"`
	Avail      uint64 `json:"avail"`
	Inodes     uint64 `json:"inodes"`
	InodesFree uint64 `json:"inodes_free"`
}

// RAID is /proc/mdstat. Present is false when the kernel has no md
// arrays (or no md driver): "no RAID", not a problem.
type RAID struct {
	Present bool      `json:"present"`
	Arrays  []MDArray `json:"arrays"`
}

// MDArray is one md array.
type MDArray struct {
	Name    string `json:"name"`   // md0
	Level   string `json:"level"`  // raid1
	Active  bool   `json:"active"` // "active" rather than "inactive"
	Devices int    `json:"devices"`
	// Want and Have are [n/m]: devices the array should have, and has
	// working. Status is the [UU_] string.
	Want   int    `json:"want"`
	Have   int    `json:"have"`
	Status string `json:"status"`
	Failed int    `json:"failed"` // members marked (F)
	// Sync is a resync/recovery/check in progress ("recovery 12.6%"), "".
	Sync string `json:"sync,omitempty"`
}

// NetIf is one interface's traffic over the last interval.
type NetIf struct {
	Name   string  `json:"name"`
	RxBps  float64 `json:"rx_bps"` // bytes per second
	TxBps  float64 `json:"tx_bps"`
	RxErrs uint64  `json:"rx_errs"` // since boot
	TxErrs uint64  `json:"tx_errs"`
}

// Updates is apt's pending updates, checked about once an hour.
type Updates struct {
	Total    int       `json:"total"`
	Security int       `json:"security"`
	Checked  time.Time `json:"checked"`
}

// Clock is the kernel's view of time synchronisation (adjtimex).
type Clock struct {
	Synced     bool    `json:"synced"`
	OffsetMs   float64 `json:"offset_ms"`
	MaxErrorMs float64 `json:"max_error_ms"`
}

// Cert is a certificate file's expiry.
type Cert struct {
	Name     string    `json:"name"` // the file's base name
	Subject  string    `json:"subject,omitempty"`
	NotAfter time.Time `json:"not_after,omitempty"`
	Err      string    `json:"err,omitempty"`
}

// MySQLStatus is what a read-only monitoring user can see.
type MySQLStatus struct {
	Up            bool   `json:"up"`
	Err           string `json:"err,omitempty"`
	Role          string `json:"role,omitempty"` // primary, replica
	Version       string `json:"version,omitempty"`
	Uptime        int64  `json:"uptime_s,omitempty"`
	ReadOnly      bool   `json:"read_only"`
	SuperReadOnly bool   `json:"super_read_only"`
	Connected     int64  `json:"threads_connected"`
	Running       int64  `json:"threads_running"`
	MaxConns      int64  `json:"max_connections"`
	PeakConns     int64  `json:"max_used_connections"`
	SlowQueries   int64  `json:"slow_queries"`
	// Replicas is how many replicas are streaming the binary log from this
	// server (Binlog Dump threads); -1 when it can't be seen (no PROCESS).
	Replicas int64          `json:"replicas"`
	Replica  *ReplicaStatus `json:"replica,omitempty"`
}

// ReplicaStatus is SHOW REPLICA STATUS, the parts that matter.
type ReplicaStatus struct {
	IORunning  string `json:"io_running"`  // Yes, No, Connecting
	SQLRunning string `json:"sql_running"` // Yes, No
	// Behind is Seconds_Behind_Source; nil for NULL (a thread stopped).
	Behind   *int64 `json:"seconds_behind,omitempty"`
	IOErrno  int64  `json:"last_io_errno,omitempty"`
	IOError  string `json:"last_io_error,omitempty"`
	SQLErrno int64  `json:"last_sql_errno,omitempty"`
	SQLError string `json:"last_sql_error,omitempty"`
	Source   string `json:"source,omitempty"` // Source_Host: a tunnel address
	// Backlog is transactions received but not applied yet
	// (Retrieved_Gtid_Set minus Executed_Gtid_Set); -1 unknown.
	Backlog int64 `json:"gtid_backlog"`
}

// RedisStatus is INFO, the parts that matter.
type RedisStatus struct {
	Up        bool   `json:"up"`
	Err       string `json:"err,omitempty"`
	Version   string `json:"version,omitempty"`
	Uptime    int64  `json:"uptime_s,omitempty"`
	Used      int64  `json:"used_memory"`
	MaxMemory int64  `json:"maxmemory"`
	Clients   int64  `json:"connected_clients"`
	Keys      int64  `json:"keys"`
	Evicted   int64  `json:"evicted_keys"`
	Rejected  int64  `json:"rejected_connections"`
}

// BridgeSnapshot is what a bridge node reports about its own process.
type BridgeSnapshot struct {
	Node     string    `json:"node"`
	Time     time.Time `json:"time"`
	Interval int       `json:"interval_s"`
	Version  string    `json:"version"`
	Started  time.Time `json:"started"`

	// Devices is how many devices have at least one connection to this
	// node; DeviceConns all those connections, Idle the ones waiting in
	// the pools; Clients the app and web sockets paired with a device
	// here (a client forwarded from the other node counts where it is
	// paired, so each counts once).
	Devices     int `json:"devices"`
	DeviceConns int `json:"device_conns"`
	Idle        int `json:"idle_conns"`
	Clients     int `json:"clients"`

	Goroutines int    `json:"goroutines"`
	FDs        int    `json:"fds"`     // open file descriptors; -1 unknown
	MaxFDs     int    `json:"max_fds"` // RLIMIT_NOFILE; -1 unknown
	HeapBytes  uint64 `json:"heap_bytes"`
	SysBytes   uint64 `json:"sys_bytes"`
	// Errors15m is ERROR lines this process logged in the last 15 minutes.
	Errors15m int `json:"errors_15m"`

	// CertNotAfter is the expiry of the certificate this node serves
	// ([otc-api] ssl-cert, as the file is now).
	CertNotAfter time.Time `json:"cert_not_after,omitempty"`
	CertErr      string    `json:"cert_err,omitempty"`
}
