// SPDX-License-Identifier: AGPL-3.0-or-later

package network

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost stands in for everything the package asks the system: nmcli,
// systemd, the clocks, the files.
type fakeHost struct {
	mu        sync.Mutex
	clock     time.Time
	up        float64
	nmRunning bool
	unitOn    bool
	devices   string
	wifiList  string
	calls     [][]string
}

func hexName(s string) string { return strings.ToUpper(hex.EncodeToString([]byte(s))) }

// setup points the package at a temporary directory and a fake host that
// manages its Wi-Fi, with nothing in flight.
func setup(t *testing.T) *fakeHost {
	t.Helper()
	t.Setenv("OTC_CONTAINER", "")
	dir := t.TempDir()
	h := &fakeHost{
		clock:     time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
		up:        5000,
		nmRunning: true,
		unitOn:    true,
		devices:   "eth0:ethernet:unavailable\nwlan0:wifi:connected\np2p-dev-wlan0:wifi-p2p:disconnected\nlo:loopback:connected (externally)\n",
		wifiList:  "yes:" + hexName("home") + ":72:WPA2\nno:" + hexName("neighbour") + ":40:WPA1 WPA2\n",
	}
	saved := struct {
		runDir, statusDir, runnerPath, netplanDir string
		nmcli                                     func(context.Context, ...string) ([]byte, error)
		now                                       func() time.Time
		uptime                                    func() float64
		runtimeDir                                func() string
		unitActive                                func(context.Context, string) bool
		writable                                  func(string) bool
	}{runDir, statusDir, runnerPath, netplanDir, nmcli, now, uptime, runtimeDir, unitActive, writable}
	t.Cleanup(func() {
		runDir, statusDir, runnerPath, netplanDir = saved.runDir, saved.statusDir, saved.runnerPath, saved.netplanDir
		nmcli, now, uptime, runtimeDir, unitActive, writable = saved.nmcli, saved.now, saved.uptime, saved.runtimeDir, saved.unitActive, saved.writable
		reset()
	})
	runDir = filepath.Join(dir, "run-otc")
	statusDir = filepath.Join(dir, "run-otc-wifi-join")
	runnerPath = filepath.Join(dir, "otc-wifi-join-runner")
	netplanDir = filepath.Join(dir, "netplan")
	for _, d := range []string{runDir, statusDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(runnerPath, []byte("#!/usr/bin/python3 -I\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nmcli = h.nmcli
	now = func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.clock }
	uptime = func() float64 { h.mu.Lock(); defer h.mu.Unlock(); return h.up }
	runtimeDir = func() string { return runDir }
	unitActive = func(_ context.Context, unit string) bool { return unit == pathUnit && h.unitOn }
	writable = func(string) bool { return true }
	reset()
	return h
}

func reset() {
	mu.Lock()
	pending, ended, accepted = nil, nil, nil
	mu.Unlock()
	forgetManaged()
}

func (h *fakeHost) advance(d time.Duration) {
	h.mu.Lock()
	h.clock = h.clock.Add(d)
	h.up += d.Seconds()
	h.mu.Unlock()
}

func (h *fakeHost) nmcli(_ context.Context, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, args)
	switch a := strings.Join(args, " "); {
	case a == "-t -g RUNNING general":
		if !h.nmRunning {
			return nil, errors.New("exit status 8")
		}
		return []byte("running\n"), nil
	case a == "-t -f DEVICE,TYPE,STATE device":
		return []byte(h.devices), nil
	case strings.HasPrefix(a, "-t -f ACTIVE,SSID-HEX,SIGNAL,SECURITY device wifi list"):
		return []byte(h.wifiList), nil
	case a == "--version":
		return []byte("nmcli tool, version 1.42.4\n"), nil
	}
	return nil, fmt.Errorf("unexpected nmcli %q", args)
}

// writeStatus writes the runner's answer as the runner does.
func writeStatus(t *testing.T, st runnerStatus) {
	t.Helper()
	st.V = 1
	raw, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(statusDir, cStatusName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	ssids := []struct {
		s  string
		ok bool
	}{
		{"", false}, {"a", true}, {strings.Repeat("x", 32), true}, {strings.Repeat("x", 33), false},
		{strings.Repeat("é", 16), true}, {strings.Repeat("é", 17), false}, {"café ☕", true},
		{"tab\there", false}, {"nl\n", false}, {"del\x7f", false}, {"bad\xff", false}, {" spaces ", true},
	}
	for _, c := range ssids {
		if got := validSSID(c.s) == nil; got != c.ok {
			t.Errorf("ssid %q: %v, want %v", c.s, got, c.ok)
		}
	}
	psks := []struct {
		s  string
		ok bool
	}{
		{"", true}, {strings.Repeat("x", 7), false}, {strings.Repeat("x", 8), true}, {strings.Repeat("x", 63), true},
		{strings.Repeat("aF0", 21) + "a", true}, {strings.Repeat("g", 64), false}, {strings.Repeat("x", 65), false},
		{strings.Repeat("ñ", 31) + "x", true}, {strings.Repeat("ñ", 32), false}, {"pass\tword", false},
		{"pass word", true}, {"bad\xffbytes", false},
	}
	for _, c := range psks {
		if got := validPSK([]byte(c.s)) == nil; got != c.ok {
			t.Errorf("psk %q: %v, want %v", c.s, got, c.ok)
		}
	}
}

func TestParseNetworks(t *testing.T) {
	out := strings.Join([]string{
		"no:" + hexName("neighbour") + ":40:WPA1 WPA2",
		"no:" + hexName("home") + ":55:WPA2",
		"yes:" + hexName("home") + ":31:WPA2", // the device is on a weaker access point of it
		"no:" + hexName("café:bar") + ":80:WPA3",
		"no::90:WPA2",               // hidden
		"no:" + "FF00" + ":95:WPA2", // not UTF-8
		"no:" + hexName("office") + ":60:WPA2 802.1X",
		"no:" + hexName("old") + ":60:WEP",
		"no:" + hexName("free") + ":60:",
		"no:" + hexName("owe") + ":10:OWE",
		// An open access point louder than the secured one with the same
		// name: still secured (the runner won't join it open).
		"no:" + hexName("mixed") + ":90:",
		"no:" + hexName("mixed") + ":30:WPA2",
		// An enterprise network and an open copy: the open one is the only
		// one the runner can join.
		"no:" + hexName("ent") + ":70:WPA2 802.1X",
		"no:" + hexName("ent") + ":20:--",
		"garbage",
	}, "\n")
	got := parseNetworks([]byte(out))
	want := []Network{
		{SSID: "home", Signal: 55, Secured: true, Current: true},
		{SSID: "mixed", Signal: 90, Secured: true},
		{SSID: "café:bar", Signal: 80, Secured: true},
		{SSID: "ent", Signal: 70},
		{SSID: "free", Signal: 60},
		{SSID: "office", Signal: 60, Secured: true, Unsupported: true},
		{SSID: "old", Signal: 60, Secured: true, Unsupported: true},
		{SSID: "neighbour", Signal: 40, Secured: true},
		{SSID: "owe", Signal: 10, Secured: true, Unsupported: true},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestCurrent(t *testing.T) {
	h := setup(t)
	h.devices = "eth0:ethernet:connected\nwlan1:wifi:disconnected\nuap0:wifi:connected\nwlan0:wifi:connected\n"
	ssid, signal, wired := Current(context.Background())
	if ssid != "home" || signal != 72 || !wired {
		t.Errorf("Current = %q %d %v", ssid, signal, wired)
	}
	if last := h.calls[len(h.calls)-1]; strings.Join(last, " ") != "-t -f ACTIVE,SSID-HEX,SIGNAL,SECURITY device wifi list ifname wlan0 --rescan no" {
		t.Errorf("asked %v", last)
	}
	h.devices = "eth0:ethernet:connected\n"
	if ssid, signal, wired := Current(context.Background()); ssid != "" || signal != 0 || !wired {
		t.Errorf("cable only: %q %d %v", ssid, signal, wired)
	}
	if n, ok := wifiIface(parseDevices([]byte("wlan1:wifi:disconnected\nwlan0:wifi:disconnected\n"))); !ok || n != "wlan0" {
		t.Errorf("wlan0 before the first: %q", n)
	}
	if _, ok := wifiIface(parseDevices([]byte("uap0:wifi:connected\np2p-dev-wlan0:wifi:disconnected\nbad/name:wifi:connected\n"))); ok {
		t.Error("the hotspot, p2p and odd names are no client interface")
	}
}

func TestManaged(t *testing.T) {
	cases := []struct {
		name   string
		change func(*fakeHost)
		want   string
	}{
		{"managed", func(*fakeHost) {}, ""},
		{"container", func(*fakeHost) { os.Setenv("OTC_CONTAINER", "1") }, "in a container"},
		{"runtime directory", func(*fakeHost) { runtimeDir = func() string { return "" } }, "runtime directory"},
		{"not writable", func(*fakeHost) { writable = func(string) bool { return false } }, "no writable"},
		{"no runner", func(*fakeHost) { os.Remove(runnerPath) }, "no Wi-Fi change runner"},
		{"netplan file", func(*fakeHost) {
			os.Mkdir(netplanDir, 0o755)
			os.WriteFile(filepath.Join(netplanDir, "01-nm.yaml"), []byte("network:\n  version: 2\n  renderer: NetworkManager\n"), 0o600)
		}, "driven by netplan"},
		{"netplan NM file", func(*fakeHost) {
			os.Mkdir(netplanDir, 0o755)
			os.WriteFile(filepath.Join(netplanDir, "90-NM-1234.yaml"), nil, 0o600)
		}, "driven by netplan"},
		{"path unit off", func(h *fakeHost) { h.unitOn = false }, "is not active"},
		{"NetworkManager off", func(h *fakeHost) { h.nmRunning = false }, "NetworkManager is not running"},
		{"no Wi-Fi", func(h *fakeHost) { h.devices = "eth0:ethernet:connected\nuap0:wifi:connected\n" }, "no Wi-Fi interface"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := setup(t)
			c.change(h)
			why := unmanagedReason()
			if (c.want == "") != (why == "") || !strings.Contains(why, c.want) {
				t.Errorf("reason %q, want %q", why, c.want)
			}
			if Managed() != (c.want == "") {
				t.Errorf("Managed() = %v", !(c.want == ""))
			}
		})
	}

	// A netplan for networkd leaves it managed; so does an empty netplan
	// directory (Raspberry Pi OS's NetworkManager is built with netplan,
	// but the image has no netplan file).
	setup(t)
	os.Mkdir(netplanDir, 0o755)
	if why := unmanagedReason(); why != "" {
		t.Errorf("empty netplan directory: %s", why)
	}
	os.Mkdir(netplanDir, 0o755)
	os.WriteFile(filepath.Join(netplanDir, "50-cloud.yaml"), []byte("network:\n  renderer: networkd\n"), 0o644)
	if why := unmanagedReason(); why != "" {
		t.Errorf("unmanaged: %s", why)
	}
}

// The answer is kept 30 seconds.
func TestManagedIsKept(t *testing.T) {
	h := setup(t)
	if !Managed() {
		t.Fatal("not managed")
	}
	h.nmRunning = false
	if !Managed() {
		t.Error("asked again within 30 s")
	}
	h.advance(31 * time.Second)
	if Managed() {
		t.Error("not asked again after 30 s")
	}
}

func readRequest(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join(runDir, cRequestName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("request mode %v", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	entries, _ := os.ReadDir(runDir)
	if len(entries) != 1 {
		t.Errorf("left beside the request: %v", entries)
	}
	return req
}

func TestJoinWritesTheRequest(t *testing.T) {
	setup(t)
	pw := []byte(`p"a\ss wörd`)
	j, err := Join(`my "net"\ ☕`, pw, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != StateJoining || j.Code != StateJoining || len(j.ID) != 16 || j.SSID != `my "net"\ ☕` {
		t.Errorf("answer %+v", j)
	}
	req := readRequest(t)
	want := map[string]any{"v": 1.0, "id": j.ID, "ssid": `my "net"\ ☕`, "psk": `p"a\ss wörd`, "hidden": true}
	if fmt.Sprint(req) != fmt.Sprint(want) {
		t.Errorf("request %v\nwant    %v", req, want)
	}
	if string(pw) != `p"a\ss wörd` {
		t.Error("the caller's password was changed")
	}
	if st := Status(); st == nil || st.ID != j.ID || st.State != StateJoining {
		t.Errorf("Status %+v", st)
	}
	if !InFlight() || !Joining() {
		t.Error("not in flight")
	}
}

func refusal(t *testing.T, err error) string {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("not a refusal: %v", err)
	}
	return r.Code
}

func TestJoinRefusals(t *testing.T) {
	h := setup(t)
	if c := refusal(t, func() error { _, err := Join("", nil, false, nil); return err }()); c != "bad_name" {
		t.Errorf("empty name: %s", c)
	}
	if c := refusal(t, func() error { _, err := Join("home", []byte("short"), false, nil); return err }()); c != "bad_password" {
		t.Errorf("short password: %s", c)
	}
	h.nmRunning = false
	if c := refusal(t, func() error { _, err := Join("home", nil, false, nil); return err }()); c != "not_managed" {
		t.Errorf("unmanaged: %s", c)
	}
	h.nmRunning = true
	forgetManaged()
	// An update asked for or running.
	if c := refusal(t, func() error { _, err := Join("home", nil, false, func() bool { return true }); return err }()); c != "update_running" {
		t.Errorf("updating: %s", c)
	}

	// One at a time: in flight, and a request already there.
	if _, err := Join("home", []byte("password1"), false, nil); err != nil {
		t.Fatal(err)
	}
	if c := refusal(t, func() error { _, err := Join("home", nil, false, nil); return err }()); c != "busy" {
		t.Errorf("in flight: %s", c)
	}
	reset()
	if c := refusal(t, func() error { _, err := Join("home", nil, false, nil); return err }()); c != "busy" {
		t.Errorf("request there: %s", c)
	}
	if req := readRequest(t); req["psk"] != "password1" {
		t.Error("the request there was replaced")
	}
}

// Six in 15 minutes; the seventh waits.
func TestJoinRateLimit(t *testing.T) {
	h := setup(t)
	for i := 0; i < 6; i++ {
		j, err := Join("home", nil, false, nil)
		if err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
		os.Remove(filepath.Join(runDir, cRequestName))
		writeStatus(t, runnerStatus{ID: j.ID, State: StateFailed, Code: "not_found", SSID: "home", Uptime: h.up})
		h.advance(time.Minute)
	}
	if c := refusal(t, func() error { _, err := Join("home", nil, false, nil); return err }()); c != "too_many" {
		t.Errorf("seventh: %s", c)
	}
	h.advance(10 * time.Minute)
	if _, err := Join("home", nil, false, nil); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

func TestStatus(t *testing.T) {
	h := setup(t)
	if Status() != nil || InFlight() || Joining() {
		t.Fatal("something reported before any change")
	}
	j, err := Join("home", []byte("password1"), false, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The runner's answer for another change doesn't count.
	writeStatus(t, runnerStatus{ID: "aaaaaaaaaaaaaaaa", State: StateFailed, Code: "wrong_password", SSID: "x", Uptime: h.up})
	if st := Status(); st == nil || st.ID != j.ID || st.State != StateJoining {
		t.Errorf("other id: %+v", st)
	}
	// Its own: joining, then the end, which wins over pending.
	os.Remove(filepath.Join(runDir, cRequestName))
	writeStatus(t, runnerStatus{ID: j.ID, State: StateJoining, Code: "joining", SSID: "home", Uptime: h.up})
	h.advance(2 * time.Minute)
	if st := Status(); st == nil || st.State != StateJoining || !InFlight() {
		t.Errorf("joining: %+v", st)
	}
	writeStatus(t, runnerStatus{ID: j.ID, State: StateFailed, Code: "wrong_password", SSID: "home", Left: true, Restored: true, Uptime: h.up})
	st := Status()
	if st == nil || st.State != StateFailed || st.Code != "wrong_password" || !st.Left || !st.Restored || InFlight() {
		t.Fatalf("ended: %+v", st)
	}
	if !strings.Contains(st.Text(""), "back on its previous network") {
		t.Errorf("text %q", st.Text(""))
	}
	if !Joining() {
		t.Error("the watchdog must keep quiet right after a change")
	}
	h.advance(601 * time.Second)
	if Joining() {
		t.Error("still quiet 10 minutes after")
	}
	if Status() == nil {
		t.Error("forgotten before 15 minutes")
	}
	h.advance(300 * time.Second)
	if Status() != nil {
		t.Error("reported after 15 minutes")
	}
}

func TestStatusCodes(t *testing.T) {
	h := setup(t)
	for _, c := range []struct{ state, code, want string }{
		{StateFailed, "exploded", "join_failed"},
		{StateFailed, "no_answer", "no_answer"},
		{StateFailed, "connected", "join_failed"},
		{StateConnected, "whatever", "connected"},
		{StateJoining, "whatever", "joining"},
	} {
		writeStatus(t, runnerStatus{ID: "0123456789abcdef", State: c.state, Code: c.code, SSID: "home", Uptime: h.up})
		if st := Status(); st == nil || st.Code != c.want {
			t.Errorf("%s/%s: %+v", c.state, c.code, st)
		}
	}
	// What doesn't read as the runner's answer is none.
	for _, raw := range []string{
		`{"v":1,"id":"../etc","state":"failed","code":"busy"}`,
		`{"v":1,"id":"0123456789abcdef","state":"rebooted"}`,
		`not json`,
		`{"v":1,"id":"0123456789abcdef","state":"failed","code":"busy","ssid":"` + strings.Repeat("x", 5000) + `"}`,
	} {
		os.WriteFile(filepath.Join(statusDir, cStatusName), []byte(raw), 0o644)
		if st := Status(); st != nil {
			t.Errorf("%.40s: %+v", raw, st)
		}
	}
	// A name over 32 bytes is dropped, the answer kept.
	writeStatus(t, runnerStatus{ID: "0123456789abcdef", State: StateFailed, Code: "busy", SSID: strings.Repeat("x", 40), Uptime: h.up})
	if st := Status(); st == nil || st.SSID != "" {
		t.Errorf("long name: %+v", st)
	}
	// restored only counts with left.
	writeStatus(t, runnerStatus{ID: "0123456789abcdef", State: StateFailed, Code: "join_failed", Restored: true, Uptime: h.up})
	if st := Status(); st == nil || st.Restored {
		t.Errorf("restored without left: %+v", st)
	}
}

// The runner never took the request (its path unit didn't fire): after a
// minute the request is withdrawn and the change ends no_answer.
func TestStatusNoAnswer(t *testing.T) {
	h := setup(t)
	j, err := Join("home", nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.advance(59 * time.Second)
	if st := Status(); st == nil || st.State != StateJoining {
		t.Fatalf("before a minute: %+v", st)
	}
	h.advance(2 * time.Second)
	st := Status()
	if st == nil || st.ID != j.ID || st.State != StateFailed || st.Code != "no_answer" || st.Left {
		t.Fatalf("no answer: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(runDir, cRequestName)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the request was left")
	}
	if InFlight() {
		t.Error("still in flight")
	}
	// The next change goes ahead.
	if _, err := Join("home", nil, false, nil); err != nil {
		t.Errorf("next: %v", err)
	}
}

// A change still "joining" ten minutes after the runner's last word was
// cut off without one (a crash, the power): interrupted, where the device
// ended up unknown. Not before: the unit gives a run 6 minutes and its
// --stopped 3 more.
func TestStatusInterrupted(t *testing.T) {
	h := setup(t)
	writeStatus(t, runnerStatus{ID: "0123456789abcdef", State: StateJoining, Code: "joining", SSID: "home", Uptime: h.up})
	h.advance(9 * time.Minute)
	if !InFlight() {
		t.Fatal("not in flight at 9 minutes")
	}
	if _, err := Join("home", nil, false, nil); err == nil {
		t.Error("joined while another change runs")
	}
	h.advance(61 * time.Second)
	st := Status()
	if st == nil || st.State != StateFailed || st.Code != "interrupted" || st.Left || InFlight() {
		t.Errorf("interrupted: %+v", st)
	}
	if !strings.HasPrefix(st.Text(""), "Connecting to") || strings.Contains(st.Text(""), "previous network") {
		t.Errorf("text %q", st.Text(""))
	}
}

func TestLogVersion(t *testing.T) {
	setup(t)
	LogVersion() // logs; must not fail without a log file
}

// An update starts only while no change is in flight, under Join's lock.
func TestUnlessChanging(t *testing.T) {
	setup(t)
	started := 0
	start := func() error { started++; return nil }
	if ran, err := UnlessChanging(start); !ran || err != nil || started != 1 {
		t.Fatalf("idle: %v %v %d", ran, err, started)
	}
	if _, err := Join("home", nil, false, nil); err != nil {
		t.Fatal(err)
	}
	if ran, _ := UnlessChanging(start); ran || started != 1 {
		t.Errorf("an update started during a change")
	}
	wantErr := errors.New("no")
	reset()
	os.Remove(filepath.Join(runDir, cRequestName))
	if ran, err := UnlessChanging(func() error { return wantErr }); !ran || err != wantErr {
		t.Errorf("its error: %v %v", ran, err)
	}
}
