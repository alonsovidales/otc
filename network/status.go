// SPDX-License-Identifier: AGPL-3.0-or-later

package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/log"
)

// Where the change is handed over and answered (package vars: the tests
// point them at a temporary directory).
var (
	// runDir is otc.service's RuntimeDirectory (a tmpfs the otc user owns,
	// emptied whenever the service stops): the request goes there.
	runDir = "/run/otc"
	// statusDir is the root runner's own directory: its answer,
	// status.json, which the otc user can read but not write.
	statusDir = "/run/otc-wifi-join"
	// runnerPath is the root runner, and pathUnit the unit that starts it
	// when the request appears.
	runnerPath = "/usr/local/bin/otc-wifi-join-runner"
	pathUnit   = "otc-wifi-join.path"
	// netplanDir: where NetworkManager is driven by netplan, the profiles
	// it saves go there, and the runner's check that a profile was saved
	// would refuse every change.
	netplanDir = "/etc/netplan"
)

const (
	cRequestName    = "wifi-join.request"
	cStatusName     = "status.json"
	cManagedFor     = 30 * time.Second
	cMaxStatusBytes = 4096
	// A change the runner hasn't started on within cNoAnswerAfter never
	// will (its path unit didn't fire): the request is withdrawn.
	cNoAnswerAfter = 60 * time.Second
	// A change still "joining" this long after its last word (seconds of
	// uptime) was cut off without its runner's --stopped answering: the
	// runner writes a word at every step (none takes 2 minutes), and its
	// unit gives --stopped 3 minutes more, so 10 never cut a live one.
	cStaleJoining = 600.0
	// How long an ended change is still reported (last_join), and how
	// long after it the Wi-Fi watchdog keeps quiet (Joining).
	cReportFor = 900.0
	cQuietFor  = 600.0
)

// Seams for the tests.
var (
	// now is the process's clock, for its own pending change (monotonic).
	now = time.Now
	// uptime is seconds since boot (/proc/uptime): the runner stamps its
	// answer with it, and the Pi has no battery-backed clock, so the wall
	// clock may jump under NTP.
	uptime = readUptime
	// runtimeDir is what systemd set as the service's RuntimeDirectory.
	runtimeDir = func() string { return os.Getenv("RUNTIME_DIRECTORY") }
	// unitActive reports whether a systemd unit is active.
	unitActive = func(ctx context.Context, unit string) bool {
		out, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
		return err == nil && strings.TrimSpace(string(out)) == "active"
	}
	// writable reports whether the service may create files in dir.
	writable = func(dir string) bool { return syscall.Access(dir, 2) == nil }
)

func readUptime() float64 {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// The states and codes of a change (WifiJoin.state / code).
const (
	StateJoining   = "joining"
	StateConnected = "connected"
	StateFailed    = "failed"

	codeNoAnswer    = "no_answer"
	codeInterrupted = "interrupted"
	codeJoinFailed  = "join_failed"
)

// JoinStatus is a change asked from Settings and how it went.
type JoinStatus struct {
	ID    string
	SSID  string
	State string
	// Code: "joining", "connected", or why it failed (devtext's Wi-Fi
	// change codes).
	Code string
	// Left: the device had left its Wi-Fi network for this one;
	// Restored: and is back on it. Both false when it never left.
	Left, Restored bool
}

// Text is the change's message in lang.
func (j JoinStatus) Text(lang string) string {
	return devtext.WifiJoinText(lang, j.Code, j.SSID, j.Left, j.Restored)
}

// pending is the change this process handed over and hasn't seen end.
type pendingJoin struct {
	id, ssid string
	at       time.Time
}

// localEnd is a change this process ended itself (the runner never
// answered), reported as the runner's answers are.
type localEnd struct {
	st JoinStatus
	// at is the uptime it ended at.
	at float64
}

var (
	mu      sync.Mutex
	pending *pendingJoin
	ended   *localEnd
	// accepted are the times of the changes handed over lately (the rate
	// limit).
	accepted []time.Time

	managedMu     sync.Mutex
	managedAt     time.Time
	managedCached bool
	managedWhy    string
)

// Managed reports whether the device changes its own Wi-Fi from Settings:
// Linux with NetworkManager running (not driven by netplan), a wireless
// interface, the root runner installed and its path unit active, and never
// in a container.
// Kept for cManagedFor; a change of answer is logged once.
func Managed() bool {
	managedMu.Lock()
	defer managedMu.Unlock()
	t := now()
	if !managedAt.IsZero() && t.Sub(managedAt) < cManagedFor && !t.Before(managedAt) {
		return managedCached
	}
	why := unmanagedReason()
	if managedAt.IsZero() || why != managedWhy {
		if why == "" {
			log.Info("network: this device changes its Wi-Fi from Settings")
		} else {
			log.Info("network: Settings > Wi-Fi is off on this device:", why)
		}
	}
	managedAt, managedCached, managedWhy = t, why == "", why
	return managedCached
}

// forgetManaged drops Managed's kept answer (tests).
func forgetManaged() {
	managedMu.Lock()
	managedAt, managedWhy = time.Time{}, ""
	managedMu.Unlock()
}

// unmanagedReason is why the device doesn't change its own Wi-Fi, "" when
// it does. Log words only.
func unmanagedReason() string {
	if container.Enabled() {
		return "in a container" // i18n-ignore: a log line's words (Managed logs the reason)
	}
	if rd := runtimeDir(); rd != runDir {
		return "the service's runtime directory is not " + runDir // i18n-ignore: a log line's words (Managed logs the reason)
	}
	if fi, err := os.Stat(runDir); err != nil || !fi.IsDir() || !writable(runDir) {
		return "no writable " + runDir // i18n-ignore: a log line's words (Managed logs the reason)
	}
	if fi, err := os.Stat(runnerPath); err != nil || !fi.Mode().IsRegular() {
		return "no Wi-Fi change runner (" + runnerPath + ")" // i18n-ignore: a log line's words (Managed logs the reason)
	}
	if why := netplanBacked(); why != "" {
		return why
	}
	ctx, cancel := context.WithTimeout(context.Background(), cQueryTimeout)
	defer cancel()
	if !unitActive(ctx, pathUnit) {
		return pathUnit + " is not active" // i18n-ignore: a log line's words (Managed logs the reason)
	}
	out, err := nmcli(ctx, "-t", "-g", "RUNNING", "general")
	if err != nil || strings.TrimSpace(string(out)) != "running" {
		return "NetworkManager is not running" // i18n-ignore: a log line's words (Managed logs the reason)
	}
	out, err = nmcli(ctx, "-t", "-f", "DEVICE,TYPE,STATE", "device")
	if err != nil {
		return "nmcli device failed" // i18n-ignore: a log line's words (Managed logs the reason)
	}
	if _, ok := wifiIface(parseDevices(out)); !ok {
		return "no Wi-Fi interface" // i18n-ignore: a log line's words (Managed logs the reason)
	}
	return ""
}

var reNetplanNM = regexp.MustCompile(`(?m)^\s*renderer:\s*["']?NetworkManager["']?\s*(#.*)?$`)

// netplanBacked is why NetworkManager counts as driven by netplan, "" when
// it isn't: a 90-NM-*.yaml (what NetworkManager writes there), or any
// netplan file with renderer NetworkManager - one that can't be read
// counts too (netplan wants them 0600 root), as it can't be ruled out.
// Raspberry Pi OS's NetworkManager is built with netplan, but its own
// patch (network-manager 1.52.1-1+rpt4, 0012-fix-netplan.patch) writes to
// netplan only profiles whose file name starts with "netplan-": the
// runner's profiles stay keyfiles. The image purges cloud-init (and with
// it rpi-cloud-init-mods' netplan file), so it has no netplan file and is
// managed; a hand-installed Raspberry Pi OS set up by cloud-init isn't.
func netplanBacked() string {
	entries, err := os.ReadDir(netplanDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		if strings.HasPrefix(name, "90-NM-") {
			return "NetworkManager is driven by netplan (" + name + ")" // i18n-ignore: a log line's words (Managed logs the reason)
		}
		raw, err := os.ReadFile(filepath.Join(netplanDir, name))
		if err != nil {
			return "a netplan file can't be read (" + name + ")" // i18n-ignore: a log line's words (Managed logs the reason)
		}
		if reNetplanNM.Match(raw) {
			return "NetworkManager is driven by netplan (" + name + ")" // i18n-ignore: a log line's words (Managed logs the reason)
		}
	}
	return ""
}

// device is one row of `nmcli -t -f DEVICE,TYPE,STATE device`.
type device struct{ name, typ, state string }

func parseDevices(out []byte) []device {
	var ds []device
	for _, line := range strings.Split(string(out), "\n") {
		f := splitUnescaped(strings.TrimRight(line, "\r"), ':')
		if len(f) < 2 || f[0] == "" {
			continue
		}
		d := device{name: unescape(f[0]), typ: unescape(f[1])}
		if len(f) > 2 {
			d.state = unescape(f[2])
		}
		ds = append(ds, d)
	}
	return ds
}

var reIface = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// wifiIface is the Wi-Fi interface a change uses, as the runner picks it:
// a client interface (never the setup hotspot's uap*, nor p2p*), the
// connected one first, then wlan0, then the first.
func wifiIface(ds []device) (string, bool) {
	var first, wlan0, connected string
	for _, d := range ds {
		if d.typ != "wifi" || !reIface.MatchString(d.name) || strings.HasPrefix(d.name, "uap") || strings.HasPrefix(d.name, "p2p") {
			continue
		}
		if first == "" {
			first = d.name
		}
		if d.name == "wlan0" {
			wlan0 = d.name
		}
		if connected == "" && strings.HasPrefix(d.state, "connected") {
			connected = d.name
		}
	}
	for _, n := range []string{connected, wlan0, first} {
		if n != "" {
			return n, true
		}
	}
	return "", false
}

// Current is the network the device is on now: the Wi-Fi network's name
// and signal ("" and 0 when not on Wi-Fi) and whether an Ethernet cable is
// connected.
func Current(ctx context.Context) (ssid string, signal int32, wired bool) {
	ctx, cancel := context.WithTimeout(ctx, cQueryTimeout)
	defer cancel()
	out, err := nmcli(ctx, "-t", "-f", "DEVICE,TYPE,STATE", "device")
	if err != nil {
		log.Debug("network: nmcli device:", err)
		return "", 0, false
	}
	ds := parseDevices(out)
	for _, d := range ds {
		if d.typ == "ethernet" && strings.HasPrefix(d.state, "connected") {
			wired = true
		}
	}
	iface, ok := wifiIface(ds)
	if !ok {
		return "", 0, wired
	}
	out, err = nmcli(ctx, "-t", "-f", "ACTIVE,SSID-HEX,SIGNAL,SECURITY", "device", "wifi", "list", "ifname", iface, "--rescan", "no")
	if err != nil {
		log.Debug("network: nmcli device wifi list:", err)
		return "", 0, wired
	}
	// The access point it is on: its own signal, not its network's
	// strongest.
	for _, line := range strings.Split(string(out), "\n") {
		f := splitUnescaped(strings.TrimRight(line, "\r"), ':')
		if len(f) < 4 || unescape(f[0]) != "yes" {
			continue
		}
		name, ok := decodeSSID(unescape(f[1]))
		if !ok {
			continue
		}
		sig, _ := strconv.ParseInt(unescape(f[2]), 10, 32)
		return name, int32(min(max(sig, 0), 100)), wired
	}
	return "", 0, wired
}

// runnerStatus is the runner's status.json.
type runnerStatus struct {
	V        int     `json:"v"`
	ID       string  `json:"id"`
	State    string  `json:"state"`
	Code     string  `json:"code"`
	SSID     string  `json:"ssid"`
	Left     bool    `json:"left"`
	Restored bool    `json:"restored"`
	Uptime   float64 `json:"uptime"`
}

var reJoinID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// readStatus reads the runner's answer: false when there is none, or it
// doesn't read as one. A code this build doesn't know is join_failed.
func readStatus() (runnerStatus, bool) {
	f, err := os.Open(filepath.Join(statusDir, cStatusName))
	if err != nil {
		return runnerStatus{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, cMaxStatusBytes+1))
	if err != nil || len(raw) > cMaxStatusBytes {
		return runnerStatus{}, false
	}
	var st runnerStatus
	if json.Unmarshal(raw, &st) != nil || !reJoinID.MatchString(st.ID) {
		return runnerStatus{}, false
	}
	if validSSID(st.SSID) != nil {
		// The name only words the message; a bad_request carries none.
		st.SSID = ""
	}
	switch st.State {
	case StateJoining:
		st.Code = StateJoining
	case StateConnected:
		st.Code = StateConnected
	case StateFailed:
		if !devtext.WifiJoinCode(st.Code) || st.Code == StateJoining || st.Code == StateConnected {
			st.Code = codeJoinFailed
		}
	default:
		return runnerStatus{}, false
	}
	if !st.Left {
		st.Restored = false
	}
	return st, true
}

func (st runnerStatus) join() JoinStatus {
	return JoinStatus{ID: st.ID, SSID: st.SSID, State: st.State, Code: st.Code, Left: st.Left, Restored: st.Restored}
}

// view is the merged picture of the last change: what to report (nil for
// nothing), whether one is in flight, and the uptime the last one ended at
// (0 for none).
type view struct {
	last     *JoinStatus
	inFlight bool
	endedAt  float64
}

// current merges this process's pending change, its own ended one and the
// runner's answer. Called with mu held.
func current() view {
	up := uptime()
	st, haveSt := readStatus()

	if p := pending; p != nil {
		switch {
		case haveSt && st.ID == p.id:
			// The runner has it: its answer is what is reported, below.
			if st.State != StateJoining || up-st.Uptime > cStaleJoining {
				pending = nil
			}
		case now().Sub(p.at) < cNoAnswerAfter:
			return view{last: &JoinStatus{ID: p.id, SSID: p.ssid, State: StateJoining, Code: StateJoining}, inFlight: true}
		default:
			// The runner never took it: its path unit didn't fire. The
			// request is this process's own, so it is withdrawn.
			if err := os.Remove(filepath.Join(runDir, cRequestName)); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Error("network: could not withdraw the Wi-Fi change request:", err)
			}
			log.Info("network: the Wi-Fi change runner never answered")
			ended = &localEnd{st: JoinStatus{ID: p.id, SSID: p.ssid, State: StateFailed, Code: codeNoAnswer}, at: up}
			pending = nil
		}
	}

	var v view
	if haveSt {
		j := st.join()
		if st.State == StateJoining {
			if up-st.Uptime > cStaleJoining {
				// Cut off without a word: the runner's --stopped would
				// have answered, so it was a crash or a power cut -
				// where the device ended up is unknown.
				j = JoinStatus{ID: st.ID, SSID: st.SSID, State: StateFailed, Code: codeInterrupted}
				v.endedAt = st.Uptime
			} else {
				v.inFlight = true
			}
		} else {
			v.endedAt = st.Uptime
		}
		if v.inFlight || up-v.endedAt <= cReportFor {
			v.last = &j
		}
	}
	// A change this process ended itself, when it ended after the
	// runner's last one.
	if e := ended; e != nil && !v.inFlight && e.at >= v.endedAt {
		v.endedAt, v.last = e.at, nil
		if up-e.at <= cReportFor {
			j := e.st
			v.last = &j
		}
	}
	return v
}

// Status is the last change asked from Settings: while it runs, and up to
// 15 minutes after it ended (nil otherwise, and after a reboot).
func Status() *JoinStatus {
	mu.Lock()
	defer mu.Unlock()
	return current().last
}

// InFlight reports whether a change is running now.
func InFlight() bool {
	mu.Lock()
	defer mu.Unlock()
	return current().inFlight
}

// Joining reports whether a change is running, or ended less than 10
// minutes ago: the Wi-Fi watchdog keeps quiet meanwhile.
func Joining() bool {
	mu.Lock()
	defer mu.Unlock()
	v := current()
	return v.inFlight || (v.endedAt > 0 && uptime()-v.endedAt < cQuietFor)
}
