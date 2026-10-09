// SPDX-License-Identifier: AGPL-3.0-or-later

package wifiwatch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSNMP(t *testing.T) {
	raw := []byte(`Ip: Forwarding DefaultTTL InReceives
Ip: 1 64 123
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 100 200 3 4 5 1000 16903 1 0 7 0
Udp: InDatagrams NoPorts
Udp: 1 2
`)
	out, retrans, err := parseSNMP(raw)
	if err != nil || out != 16903 || retrans != 1 {
		t.Fatalf("got %d %d %v", out, retrans, err)
	}
	if _, _, err := parseSNMP([]byte("Ip: a\nIp: 1\n")); err == nil {
		t.Fatal("no Tcp lines should be an error")
	}
}

func TestDefaultRouteIface(t *testing.T) {
	raw := []byte(`Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	0032A8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
wlan0	00000000	0132A8C0	0003	0	0	600	00000000	0	0	0
eth0	00000000	0132A8C0	0003	0	0	100	00000000	0	0	0
`)
	if got := defaultRouteIface(raw); got != "eth0" {
		t.Fatalf("lowest metric default route: got %q", got)
	}
	raw = []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nwlan0\t00000000\t0132A8C0\t0003\t0\t0\t600\t00000000\n")
	if got := defaultRouteIface(raw); got != "wlan0" {
		t.Fatalf("got %q", got)
	}
	if got := defaultRouteIface([]byte("Iface\tDestination\n")); got != "" {
		t.Fatalf("no default route: got %q", got)
	}
}

func TestDefaultWirelessIfaceReadsSysfs(t *testing.T) {
	dir := t.TempDir()
	oldP, oldS := procRoot, sysRoot
	procRoot, sysRoot = filepath.Join(dir, "proc"), filepath.Join(dir, "sys")
	t.Cleanup(func() { procRoot, sysRoot = oldP, oldS })
	write := func(rel, s string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nwlan0\t00000000\t0132A8C0\t0003\t0\t0\t600\t00000000\n")
	write("sys/class/net/wlan0/statistics/tx_bytes", "100\n")
	if got := defaultWirelessIface(); got != "" {
		t.Fatalf("not wireless yet: got %q", got)
	}
	_ = os.MkdirAll(filepath.Join(dir, "sys/class/net/wlan0/wireless"), 0o755)
	if got := defaultWirelessIface(); got != "wlan0" {
		t.Fatalf("got %q", got)
	}
	if readCounter("wlan0", "tx_bytes") != 100 || readCounter("wlan0", "nope") != 0 {
		t.Fatal("counters")
	}
}

func TestParseStationDump(t *testing.T) {
	out := []byte(`Station aa:bb:cc:dd:ee:ff (on wlan0)
	inactive time:	30 ms
	rx bytes:	123
	tx packets:	5000
	tx retries:	10
	tx failed:	300
	signal:  	-55 dBm
`)
	tx, failed, ok := parseStationDump(out)
	if !ok || tx != 5000 || failed != 300 {
		t.Fatalf("got %d %d %v", tx, failed, ok)
	}
	if _, _, ok := parseStationDump([]byte("")); ok {
		t.Fatal("empty dump is not ok")
	}
}

// sim is a fake device: counters that grow by what each minute sends.
type sim struct {
	t   time.Time
	cur sample
}

func newSim() *sim {
	start := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	return &sim{t: start, cur: sample{at: start, iface: "wlan0", staOK: true}}
}

// minute moves the clock a minute and adds one minute of traffic over the
// Wi-Fi: segs segments, retrans of them retransmitted, txErr tx errors,
// and lo loopback packets (also counted in OutSegs).
func (s *sim) minute(segs, retrans, txErr, lo uint64) {
	s.t = s.t.Add(time.Minute)
	s.cur.at = s.t
	s.cur.outSegs += segs + lo
	s.cur.loTxPackets += lo
	s.cur.retransSegs += retrans
	s.cur.txBytes += segs * 1400
	s.cur.txErrors += txErr
	s.cur.staTxPackets += segs
}

type harness struct {
	t        *testing.T
	sim      *sim
	updating bool
	w        *Watchdog
	dir      string
	alerts   []string
	alertOK  bool
	runner   bool
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, sim: newSim(), dir: t.TempDir(), alertOK: true, runner: true}
	h.w = h.watchdog()
	return h
}

// watchdog builds a Watchdog on the harness's files, as a fresh otc
// process would.
func (h *harness) watchdog() *Watchdog {
	w := &Watchdog{
		cfg:  DefaultConfig(),
		now:  func() time.Time { return h.sim.t },
		read: func(time.Time) (sample, error) { return h.sim.cur, nil },
		alert: func(title, detail string) error {
			if !h.alertOK {
				return errors.New("database down")
			}
			h.alerts = append(h.alerts, title+": "+detail)
			return nil
		},
		statePath:       filepath.Join(h.dir, stateFile),
		runnerInstalled: func() bool { return h.runner },
		updating:        func() bool { return h.updating },
		requestPath:     filepath.Join(h.dir, "wifi-restart.request"),
		statusPath:      filepath.Join(h.dir, "wifi-restart-status.json"),
	}
	w.load()
	w.tick() // the first look only takes a baseline
	return w
}

// run ticks n minutes of the same traffic.
func (h *harness) run(n int, segs, retrans, txErr uint64) {
	for i := 0; i < n; i++ {
		h.sim.minute(segs, retrans, txErr, 0)
		h.w.tick()
	}
}

func (h *harness) requested() bool {
	_, err := os.Stat(h.w.requestPath)
	return err == nil
}

// rootAnswers plays the root runner: removes the request, writes status.
func (h *harness) rootAnswers(state, msg string) {
	h.t.Helper()
	_ = os.Remove(h.w.requestPath)
	b, _ := json.Marshal(rootStatus{State: state, Message: msg, Updated: h.sim.t.UTC().Format(time.RFC3339)})
	if err := os.WriteFile(h.w.statusPath, b, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) phase(want string) {
	h.t.Helper()
	if h.w.st.Phase != want {
		h.t.Fatalf("phase %q, want %q", h.w.st.Phase, want)
	}
	raw, err := os.ReadFile(h.w.statePath)
	if want == phaseArmed && err != nil {
		return // never saved is armed
	}
	var st State
	if err != nil || json.Unmarshal(raw, &st) != nil || st.Phase != want {
		h.t.Fatalf("persisted phase %q (%v), want %q", st.Phase, err, want)
	}
}

// Pit's numbers: 6000 segments a minute, 7.7% retransmitted.
const (
	pitSegs     = 6000
	pitRetrans  = 462
	goodRetrans = 1
)

func TestIdleDeviceNeverTriggers(t *testing.T) {
	h := newHarness(t)
	// Half of everything retransmitted, but only 200 segments a minute.
	h.run(120, 200, 100, 5)
	if h.requested() || len(h.alerts) > 0 {
		t.Fatal("an idle device must never trigger")
	}
	h.phase(phaseArmed)
}

func TestEthernetNeverTriggers(t *testing.T) {
	h := newHarness(t)
	h.sim.cur.iface = ""
	h.run(60, pitSegs, pitRetrans, 50)
	if h.requested() {
		t.Fatal("no restart off Wi-Fi")
	}
}

func TestLoopbackDoesNotHideTheWiFi(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		// 100,000 loopback segments a minute would dilute 7.7% to 0.4%.
		h.sim.minute(pitSegs, pitRetrans, 20, 100000)
		h.w.tick()
	}
	if !h.requested() {
		t.Fatal("loopback traffic hid a stuck Wi-Fi")
	}
}

func TestCongestionWithoutRadioFailuresWaitsLonger(t *testing.T) {
	h := newHarness(t)
	h.run(9, pitSegs, pitRetrans, 0)
	if h.requested() {
		t.Fatal("uncorroborated: 9 bad minutes must not trigger")
	}
	h.run(1, pitSegs, pitRetrans, 0)
	if !h.requested() {
		t.Fatal("uncorroborated: 10 bad minutes trigger")
	}
	h.phase(phaseRestarting)
}

func TestMiddlingAndShortBurstsDoNotTrigger(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 30; i++ {
		h.run(4, pitSegs, pitRetrans, 20) // 4 bad minutes...
		h.run(1, pitSegs, goodRetrans, 0) // ...broken by a healthy one
	}
	h.run(60, pitSegs, 150, 20) // 2.5%: in between
	if h.requested() {
		t.Fatal("bursts and in-between minutes must not trigger")
	}
}

func TestQuietMinutesHoldAStreakButNotForever(t *testing.T) {
	h := newHarness(t)
	h.run(3, pitSegs, pitRetrans, 20)
	h.run(5, 10, 0, 0) // a quiet pause
	h.run(2, pitSegs, pitRetrans, 20)
	if !h.requested() {
		t.Fatal("a short pause should not reset the bad streak")
	}

	h = newHarness(t)
	h.run(4, pitSegs, pitRetrans, 20)
	h.run(11, 10, 0, 0) // longer than MaxGap
	h.run(4, pitSegs, pitRetrans, 20)
	if h.requested() {
		t.Fatal("a long pause resets the streak")
	}
}

// The whole once-per-episode story: restart, still bad, give up, no second
// restart, healthy again, re-armed, the next episode restarts again.
func TestOncePerEpisode(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	if !h.requested() {
		t.Fatal("5 corroborated bad minutes must ask for a restart")
	}
	h.phase(phaseRestarting)
	if len(h.alerts) != 0 {
		t.Fatalf("no Alert before the restart has happened: %v", h.alerts)
	}

	// Minutes while the root side works don't count.
	h.run(3, pitSegs, pitRetrans, 20)
	h.phase(phaseRestarting)

	h.rootAnswers("done", "The Wi-Fi was restarted")
	h.run(1, 0, 0, 0)
	h.phase(phaseRestarted)

	// Still bad after the settle time: give up, one Alert, no new request.
	h.run(2+5, pitSegs, pitRetrans, 20)
	h.phase(phaseGaveUp)
	if len(h.alerts) != 1 || !strings.HasPrefix(h.alerts[0], "The Wi-Fi is still slow after a restart") ||
		!strings.Contains(h.alerts[0], "Restart the device from Settings") {
		t.Fatalf("alerts: %v", h.alerts)
	}
	if h.requested() {
		t.Fatal("asked again within the episode")
	}
	h.run(120, pitSegs, pitRetrans, 20)
	if h.requested() || len(h.alerts) != 1 {
		t.Fatalf("a second restart or Alert in the same episode: %v", h.alerts)
	}

	// An otc restart in the middle keeps the episode.
	h.w = h.watchdog()
	h.phase(phaseGaveUp)
	h.run(30, pitSegs, pitRetrans, 20)
	if h.requested() {
		t.Fatal("an otc restart re-armed the watchdog")
	}

	// Healthy, sustained, with traffic: re-armed.
	h.run(4, pitSegs, goodRetrans, 0)
	h.phase(phaseGaveUp)
	h.run(1, pitSegs, goodRetrans, 0)
	h.phase(phaseArmed)

	// The next episode gets its restart.
	h.run(5, pitSegs, pitRetrans, 20)
	if !h.requested() {
		t.Fatal("a later episode must restart again")
	}
}

func TestRestartThatHelpsAlertsOnceTheLinkIsBack(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	// otc restarted while the root side worked (the state is on disk).
	h.w = h.watchdog()
	h.phase(phaseRestarting)
	h.rootAnswers("done", "The Wi-Fi was restarted")
	h.run(1, 0, 0, 0)
	h.phase(phaseRestarted)
	if len(h.alerts) != 0 {
		t.Fatal("no Alert until the link has been judged")
	}
	// Bad minutes during the settle time are ignored.
	h.run(2, pitSegs, pitRetrans, 20)
	h.run(4, pitSegs, goodRetrans, 0)
	if len(h.alerts) != 0 {
		t.Fatal("4 healthy minutes are not enough")
	}
	h.run(1, pitSegs, goodRetrans, 0)
	h.phase(phaseArmed)
	if len(h.alerts) != 1 || !strings.HasPrefix(h.alerts[0], "The Wi-Fi was restarted: sending data had slowed down to about 140 KB/s, with 7.7% of it sent twice.") ||
		!strings.HasSuffix(h.alerts[0], "It is working normally again.") {
		t.Fatalf("alerts: %v", h.alerts)
	}
}

func TestRebootFallbackIsMentioned(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	h.rootAnswers("rebooted", "The Wi-Fi did not reconnect after its restart, so the device restarted")
	h.w = h.watchdog() // the device came back up
	h.run(1, 0, 0, 0)
	h.phase(phaseRestarted)
	h.run(2+5, pitSegs, goodRetrans, 0)
	if len(h.alerts) != 1 || !strings.Contains(h.alerts[0], "so the device restarted") {
		t.Fatalf("alerts: %v", h.alerts)
	}
}

func TestRestartWithoutTrafficAfterwardsStillTellsTheOwner(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	h.rootAnswers("done", "ok")
	h.run(59, 10, 0, 0)
	if len(h.alerts) != 0 {
		t.Fatal("too early")
	}
	h.run(2, 10, 0, 0)
	if len(h.alerts) != 1 || !strings.Contains(h.alerts[0], "There hasn't been enough traffic since") {
		t.Fatalf("alerts: %v", h.alerts)
	}
	h.phase(phaseRestarted)
	h.run(30, 10, 0, 0)
	if len(h.alerts) != 1 {
		t.Fatal("told twice")
	}
	// Judged healthy later: re-armed, nothing more to say.
	h.run(5, pitSegs, goodRetrans, 0)
	h.phase(phaseArmed)
	if len(h.alerts) != 1 {
		t.Fatalf("alerts: %v", h.alerts)
	}
}

func TestRootFailureGivesUp(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	h.rootAnswers("failed", "the device isn't connected over Wi-Fi")
	h.run(1, 0, 0, 0)
	h.phase(phaseGaveUp)
	if len(h.alerts) != 1 || !strings.HasPrefix(h.alerts[0], "The Wi-Fi could not be restarted") ||
		!strings.Contains(h.alerts[0], "isn't connected over Wi-Fi") {
		t.Fatalf("alerts: %v", h.alerts)
	}
}

func TestRootSilenceGivesUp(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	h.run(9, pitSegs, pitRetrans, 20)
	h.phase(phaseRestarting)
	h.run(1, pitSegs, pitRetrans, 20)
	h.phase(phaseGaveUp)
	if h.requested() {
		t.Fatal("the unanswered request should be withdrawn")
	}
	if len(h.alerts) != 1 || !strings.Contains(h.alerts[0], "never answered") {
		t.Fatalf("alerts: %v", h.alerts)
	}
}

func TestStaleRootAnswerIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	// An answer dated well before the request (a file left from an old
	// run, restored somehow): not this request's.
	b, _ := json.Marshal(rootStatus{State: "done", Updated: h.sim.t.Add(-time.Hour).UTC().Format(time.RFC3339)})
	_ = os.WriteFile(h.w.statusPath, b, 0o644)
	h.run(1, 0, 0, 0)
	h.phase(phaseRestarting)
}

func TestNoRunnerGivesUpWithAnAlert(t *testing.T) {
	h := newHarness(t)
	h.runner = false
	h.run(5, pitSegs, pitRetrans, 20)
	if h.requested() {
		t.Fatal("no request without a runner")
	}
	h.phase(phaseGaveUp)
	if len(h.alerts) != 1 || !strings.Contains(h.alerts[0], "until it is updated") {
		t.Fatalf("alerts: %v", h.alerts)
	}
}

func TestAlertRefusedIsKeptAndRetried(t *testing.T) {
	h := newHarness(t)
	h.run(5, pitSegs, pitRetrans, 20)
	h.rootAnswers("failed", "nope")
	h.alertOK = false
	h.run(1, 0, 0, 0)
	if len(h.alerts) != 0 || h.w.st.Pending == nil {
		t.Fatal("the Alert should be pending")
	}
	// Survives an otc restart, and goes once the database takes it.
	h.w = h.watchdog()
	h.alertOK = true
	h.run(1, 0, 0, 0)
	if len(h.alerts) != 1 || h.w.st.Pending != nil {
		t.Fatalf("alerts: %v, pending %v", h.alerts, h.w.st.Pending)
	}
	h.run(3, 0, 0, 0)
	if len(h.alerts) != 1 {
		t.Fatal("raised twice")
	}
}

func TestCountersResetAfterTheDriverReload(t *testing.T) {
	prev := sample{at: time.Unix(0, 0), iface: "wlan0", outSegs: 10000, txBytes: 1 << 30}
	cur := sample{at: time.Unix(60, 0), iface: "wlan0", outSegs: 20000, retransSegs: 5000, txBytes: 100}
	if w := newWindow(prev, cur, DefaultConfig()); w.kind != kindIdle {
		t.Fatalf("a counter that went back must not make a window: %v", w.kind)
	}
}

func TestLoadConfigDefaultsWithoutSection(t *testing.T) {
	c := DefaultConfig()
	if c.BadPct != 4 || c.HealthyPct != 1 || c.BadMinutes != 5 || c.BadMinutesAlone != 10 || c.MinSegments != 1000 {
		t.Fatalf("defaults changed: %+v", c)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate(strings.Repeat("é", 150), 201); !strings.HasSuffix(got, "…") || len(got) > 204 {
		t.Fatalf("got %q", got)
	}
	if truncate(" short ", 10) != "short" {
		t.Fatal("short")
	}
}

func TestRestartWaitsForARunningUpdate(t *testing.T) {
	h := newHarness(t)
	h.updating = true
	h.run(20, pitSegs, pitRetrans, 20)
	if h.requested() {
		t.Fatal("restarted the Wi-Fi in the middle of an update")
	}
	h.updating = false
	h.run(1, pitSegs, pitRetrans, 20)
	if !h.requested() {
		t.Fatal("the restart should go once the update is over")
	}
}

func TestReadTCPOnThisMachine(t *testing.T) {
	if _, err := os.Stat("/proc/net/snmp"); err != nil {
		t.Skip("no /proc/net/snmp here")
	}
	out, _, err := readTCP()
	if err != nil || out == 0 {
		t.Fatalf("got %d, %v", out, err)
	}
	if _, err := readSample(time.Now()); err != nil {
		t.Fatal(err)
	}
}
