// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wifiwatch restarts the Wi-Fi of a device whose Wi-Fi has stopped
// sending properly, once per episode.
//
// Seen on Pit (a Pi 5 on Wi-Fi) after about 27 hours up: the brcmfmac chip
// over SDIO ("CMD53 sg block write failed -84", "brcmf_sdio_txfail")
// kept receiving at 6.6 MB/s but sent at 0.15-0.18 MB/s, with 7.7% of TCP
// segments retransmitted, at -55 dBm with power saving off. A reboot fixed
// it (8-12 MB/s, 1 retransmit in 16,903). Images were painfully slow until
// then.
//
// What it watches, once a minute, only while the default route goes over a
// wireless interface (nothing at all on Ethernet): the share of TCP
// segments retransmitted (/proc/net/snmp RetransSegs/OutSegs, loopback's
// packets taken off), only in minutes with real traffic, so an idle device
// never counts; and, to tell a stuck radio from a congested internet, the
// interface's own transmit errors and drops (brcmfmac counts a failed SDIO
// write as a tx error) and the frames the access point never acknowledged
// (`iw dev <if> station dump`). See Config for the thresholds.
//
// What it does: asks the root side to restart the Wi-Fi (the device drops
// /var/lib/otc/wifi-restart.request; otc-wifi-restart.path starts
// scripts/device-runner/otc-wifi-restart-runner.sh as root, which reloads
// the driver and resets the chip's SDIO bus). The owner's rule: one
// restart per episode. If the problem is still there after it, it is not
// restarted again; only once the link has been seen healthy again
// (sustained, with traffic) is the watchdog re-armed. The phase is kept in
// <working dir>/.wifi-watchdog.json, so it survives an otc restart and the
// Wi-Fi restart itself. The Alert comes after the restart, once the link
// has been judged: "restarted, working normally again", or that it gave up.
// Main instance only (websocket.Init).
package wifiwatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/updater"
)

const (
	stateFile = ".wifi-watchdog.json"

	// The trigger and the answer of the root side (otc-wifi-restart.path).
	cRequestPath = "/var/lib/otc/wifi-restart.request"
	cStatusPath  = "/var/lib/otc/wifi-restart-status.json"
	cRootRunner  = "/usr/local/bin/otc-wifi-restart-runner"
)

// Phases of the once-per-episode rule.
const (
	// Watching; a sustained bad link asks for a restart.
	phaseArmed = "armed"
	// The restart was asked for; waiting for the root side's answer.
	phaseRestarting = "restarting"
	// Restarted; judging the link before saying anything to the owner.
	phaseRestarted = "restarted"
	// Restarted already (or it couldn't be) and the link is still bad:
	// no more restarts until the link has been healthy for a while.
	phaseGaveUp = "gave_up"
)

// Config holds the thresholds; every one can be set in [otc] (keys in
// loadConfig). The defaults come from Pit's numbers above: bad was 7.7%
// retransmitted, healthy 0.006%. Ordinary internet loss stays under 1-2%,
// so 4% for five minutes with the radio itself reporting failures - or for
// ten minutes without - is a stuck link and not a slow website.
type Config struct {
	// A minute counts only with at least this many TCP segments sent over
	// the Wi-Fi (about 17 a second): an idle device never triggers.
	MinSegments uint64
	// Retransmitted share, in percent, at or above which a minute is bad,
	// and below which it is healthy (in between holds whatever streak is
	// running without adding to it).
	BadPct     float64
	HealthyPct float64
	// Bad minutes in a row that trigger a restart when the radio confirms
	// it (Corroborated), and without that confirmation.
	BadMinutes      int
	BadMinutesAlone int
	// Healthy minutes in a row that count as "working normally again".
	HealthyMinutes int
	// Quiet or in-between minutes a streak survives; one more resets it.
	MaxGap int
	// Settle is how long after a restart minutes are ignored (connections
	// coming back retransmit), RootWait how long the root side may take,
	// VerdictWait how long a restart waits for enough traffic to be judged
	// before the owner is told it happened anyway.
	Settle      time.Duration
	RootWait    time.Duration
	VerdictWait time.Duration
	// The radio confirms: this many tx errors + drops on the interface
	// during the bad streak, or at least FailedPct of the frames sent
	// never acknowledged by the access point.
	MinTxErrors uint64
	FailedPct   float64
}

// DefaultConfig is what a device runs with when [otc] says nothing.
func DefaultConfig() Config {
	return Config{
		MinSegments:     1000,
		BadPct:          4,
		HealthyPct:      1,
		BadMinutes:      5,
		BadMinutesAlone: 10,
		HealthyMinutes:  5,
		MaxGap:          10,
		Settle:          2 * time.Minute,
		RootWait:        10 * time.Minute,
		VerdictWait:     time.Hour,
		MinTxErrors:     10,
		FailedPct:       5,
	}
}

// loadConfig reads the overrides from [otc]. "wifi-watchdog = off" turns
// the whole thing off.
func loadConfig() (Config, bool) {
	c := DefaultConfig()
	if !cfg.HasSection("otc") {
		return c, true
	}
	get := func(key string) string { return strings.TrimSpace(cfg.GetStr("otc", key)) }
	switch strings.ToLower(get("wifi-watchdog")) {
	case "off", "false", "0", "no":
		return c, false
	}
	setU := func(key string, dst *uint64) {
		if v, err := strconv.ParseUint(get(key), 10, 64); err == nil && v > 0 {
			*dst = v
		}
	}
	setI := func(key string, dst *int) {
		if v, err := strconv.Atoi(get(key)); err == nil && v > 0 {
			*dst = v
		}
	}
	setF := func(key string, dst *float64) {
		if v, err := strconv.ParseFloat(get(key), 64); err == nil && v > 0 {
			*dst = v
		}
	}
	setU("wifi-watchdog-min-segments", &c.MinSegments)
	setF("wifi-watchdog-bad-pct", &c.BadPct)
	setF("wifi-watchdog-healthy-pct", &c.HealthyPct)
	setI("wifi-watchdog-bad-minutes", &c.BadMinutes)
	setI("wifi-watchdog-bad-minutes-alone", &c.BadMinutesAlone)
	setI("wifi-watchdog-healthy-minutes", &c.HealthyMinutes)
	if c.HealthyPct > c.BadPct {
		c.HealthyPct = c.BadPct
	}
	if c.BadMinutesAlone < c.BadMinutes {
		c.BadMinutesAlone = c.BadMinutes
	}
	return c, true
}

// window kinds.
type kind int

const (
	kindNone     kind = iota // the default route isn't wireless
	kindIdle                 // too little traffic to say, or counters reset
	kindHealthy              // traffic, and under HealthyPct retransmitted
	kindMiddling             // traffic, between HealthyPct and BadPct
	kindBad                  // traffic, BadPct or more retransmitted
)

// window is what happened between two samples.
type window struct {
	kind       kind
	iface      string
	segs       uint64 // TCP segments sent over the Wi-Fi (estimated)
	retrans    uint64
	pct        float64
	txRate     float64 // bytes a second out of the interface
	txErrors   uint64  // tx errors + drops
	staTx      uint64
	staFailed  uint64
	staCounted bool
}

func delta(a, b uint64) (uint64, bool) {
	if b < a {
		return 0, false
	}
	return b - a, true
}

func newWindow(prev, cur sample, c Config) window {
	w := window{iface: cur.iface}
	if cur.iface == "" || prev.iface != cur.iface {
		if cur.iface == "" {
			w.kind = kindNone
		} else {
			w.kind = kindIdle
		}
		return w
	}
	out, ok1 := delta(prev.outSegs, cur.outSegs)
	retrans, ok2 := delta(prev.retransSegs, cur.retransSegs)
	lo, ok3 := delta(prev.loTxPackets, cur.loTxPackets)
	txBytes, ok4 := delta(prev.txBytes, cur.txBytes)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		// Counters started again (the driver was reloaded): no window.
		w.kind = kindIdle
		return w
	}
	if lo < out {
		w.segs = out - lo
	}
	w.retrans = retrans
	if secs := cur.at.Sub(prev.at).Seconds(); secs > 0 {
		w.txRate = float64(txBytes) / secs
	}
	errs, _ := delta(prev.txErrors+prev.txDropped, cur.txErrors+cur.txDropped)
	w.txErrors = errs
	if prev.staOK && cur.staOK {
		tx, okTx := delta(prev.staTxPackets, cur.staTxPackets)
		failed, okF := delta(prev.staTxFailed, cur.staTxFailed)
		if okTx && okF {
			w.staTx, w.staFailed, w.staCounted = tx, failed, true
		}
	}
	if w.segs < c.MinSegments {
		w.kind = kindIdle
		return w
	}
	w.pct = 100 * float64(retrans) / float64(w.segs)
	switch {
	case w.pct >= c.BadPct:
		w.kind = kindBad
	case w.pct < c.HealthyPct:
		w.kind = kindHealthy
	default:
		w.kind = kindMiddling
	}
	return w
}

// streak follows consecutive bad or healthy minutes.
type streak struct {
	bad, healthy, gap int
	// Over the bad minutes of the streak.
	segs, retrans  uint64
	txErrors       uint64
	staTx, staFail uint64
	peakTx         float64
}

func (k *streak) reset() { *k = streak{} }

func (k *streak) resetBad() {
	k.bad, k.segs, k.retrans, k.txErrors, k.staTx, k.staFail, k.peakTx = 0, 0, 0, 0, 0, 0, 0
}

func (k *streak) add(w window, c Config) {
	switch w.kind {
	case kindNone:
		k.reset()
		return
	case kindBad:
		k.healthy, k.gap = 0, 0
		k.bad++
		k.segs += w.segs
		k.retrans += w.retrans
		k.txErrors += w.txErrors
		if w.staCounted {
			k.staTx += w.staTx
			k.staFail += w.staFailed
		}
		if w.txRate > k.peakTx {
			k.peakTx = w.txRate
		}
		return
	case kindHealthy:
		k.resetBad()
		k.gap = 0
		k.healthy++
		return
	case kindMiddling:
		k.healthy = 0
	}
	k.gap++
	if k.gap > c.MaxGap {
		k.reset()
	}
}

// corroborated: the radio itself reports failures, not only TCP.
func (k *streak) corroborated(c Config) bool {
	if k.txErrors >= c.MinTxErrors {
		return true
	}
	return k.staTx >= 100 && float64(k.staFail)*100 >= c.FailedPct*float64(k.staTx)
}

func (k *streak) triggered(c Config) bool {
	return (k.bad >= c.BadMinutes && k.corroborated(c)) || k.bad >= c.BadMinutesAlone
}

func (k *streak) recovered(c Config) bool { return k.healthy >= c.HealthyMinutes }

func (k *streak) pct() float64 {
	if k.segs == 0 {
		return 0
	}
	return 100 * float64(k.retrans) / float64(k.segs)
}

// describe says how bad the streak was, for the Alert: "0.17 MB/s, with
// 7.7% of it sent twice". The rate is the best minute's: with the link
// stuck, that is about what it could still do.
func (k *streak) describe() string {
	return fmt.Sprintf("%s, with %.1f%% of it sent twice", rate(k.peakTx), k.pct())
}

func rate(bytesPerSec float64) string {
	switch {
	case bytesPerSec >= 1e6:
		return fmt.Sprintf("%.2f MB/s", bytesPerSec/1e6)
	default:
		return fmt.Sprintf("%.0f KB/s", bytesPerSec/1e3)
	}
}

// State is what survives a restart of otc and of the Wi-Fi.
type State struct {
	Phase       string    `json:"phase"`
	Since       time.Time `json:"since"`
	Iface       string    `json:"iface,omitempty"`
	RequestedAt time.Time `json:"requested_at,omitempty"`
	RestartedAt time.Time `json:"restarted_at,omitempty"`
	// How the link was before the restart (streak.describe).
	Slow string `json:"slow,omitempty"`
	// What the root side said: "rebooted" when the Wi-Fi didn't come
	// back and it restarted the device instead.
	RootNote string `json:"root_note,omitempty"`
	// Alerted: the owner has been told about this restart already (one
	// that couldn't be judged within VerdictWait).
	Alerted bool `json:"alerted,omitempty"`
	// An Alert decided but not yet stored (the database refused it): it
	// is retried every minute until it is.
	Pending *PendingAlert `json:"pending_alert,omitempty"`
}

// PendingAlert is one Alert waiting to be raised.
type PendingAlert struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// rootStatus is what otc-wifi-restart-runner writes.
type rootStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
	Updated string `json:"updated"`
}

// Watchdog is the state machine; its fields are the seams tests replace.
type Watchdog struct {
	cfg       Config
	now       func() time.Time
	read      func(time.Time) (sample, error)
	alert     func(title, detail string) error
	statePath string
	// The root side: whether it is installed, the trigger and its answer.
	runnerInstalled func() bool
	// updating: an update is running (it downloads; a Wi-Fi restart
	// in the middle would fail it). The restart waits for it to end.
	updating    func() bool
	requestPath string
	statusPath  string

	st          State
	prev        *sample
	k           streak
	readErrLog  time.Time
	stillBadLog time.Time
}

// Watch runs the watchdog for the life of the process, a look a minute.
// alert stores an Alert (dao.AddErrorNotification).
func Watch(alert func(title, detail string) error) {
	c, on := loadConfig()
	if !on {
		log.Info("wifi watchdog: off ([otc] wifi-watchdog)")
		return
	}
	wd, err := os.Getwd()
	if err != nil {
		log.Error("wifi watchdog: no working directory, not started:", err)
		return
	}
	w := &Watchdog{
		cfg:             c,
		now:             time.Now,
		read:            readSample,
		alert:           alert,
		statePath:       filepath.Join(wd, stateFile),
		runnerInstalled: func() bool { _, err := os.Stat(cRootRunner); return err == nil },
		updating:        func() bool { return updater.CurrentStatus().State == "running" },
		requestPath:     cRequestPath,
		statusPath:      cStatusPath,
	}
	w.load()
	log.Info(fmt.Sprintf("wifi watchdog: %s (bad: %.1f%% retransmitted for %d minutes with the radio reporting failures or %d without, with at least %d segments a minute; healthy: under %.1f%% for %d minutes)",
		w.st.Phase, c.BadPct, c.BadMinutes, c.BadMinutesAlone, c.MinSegments, c.HealthyPct, c.HealthyMinutes))
	for {
		w.tick()
		time.Sleep(time.Minute)
	}
}

// load reads the persisted phase; anything unreadable is a fresh, armed
// watchdog.
func (w *Watchdog) load() {
	w.st = State{Phase: phaseArmed, Since: w.now()}
	raw, err := os.ReadFile(w.statePath)
	if err != nil {
		return
	}
	var st State
	if json.Unmarshal(raw, &st) != nil {
		log.Error("wifi watchdog: unreadable state, starting armed")
		return
	}
	switch st.Phase {
	case phaseArmed, phaseRestarting, phaseRestarted, phaseGaveUp:
		w.st = st
	default:
		log.Error("wifi watchdog: unknown phase", st.Phase, "- starting armed")
	}
}

// save writes the state beside itself and renames it into place.
func (w *Watchdog) save() {
	raw, err := json.Marshal(w.st)
	if err != nil {
		return
	}
	tmp := w.statePath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_, err = f.Write(raw)
		if serr := f.Sync(); err == nil {
			err = serr
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = os.Rename(tmp, w.statePath)
	}
	if err != nil {
		_ = os.Remove(tmp)
		log.Error("wifi watchdog: could not record its state:", err)
	}
}

func (w *Watchdog) setPhase(phase string) {
	w.st.Phase = phase
	w.st.Since = w.now()
	w.k.reset()
	w.save()
}

// tick takes a sample and moves the state machine on by one window.
func (w *Watchdog) tick() {
	now := w.now()
	w.flushAlert()

	if w.st.Phase == phaseRestarting {
		w.checkRoot(now)
	}

	s, err := w.read(now)
	if err != nil {
		if now.Sub(w.readErrLog) > time.Hour {
			log.Error("wifi watchdog: could not read the TCP counters:", err)
			w.readErrLog = now
		}
		w.prev = nil
		return
	}
	prev := w.prev
	w.prev = &s
	if prev == nil {
		return
	}
	win := newWindow(*prev, s, w.cfg)
	w.step(now, win)
}

// step feeds one window to the phase the watchdog is in.
func (w *Watchdog) step(now time.Time, win window) {
	switch w.st.Phase {
	case phaseRestarting:
		// Waiting for the root side; nothing measured now means anything.
		return
	case phaseRestarted:
		if now.Before(w.st.RestartedAt) {
			// The clock went back (no battery-backed clock on a Pi).
			w.st.RestartedAt = now
			w.save()
		}
		if now.Sub(w.st.RestartedAt) < w.cfg.Settle {
			return
		}
	}

	w.k.add(win, w.cfg)
	w.logWindow(win)

	switch w.st.Phase {
	case phaseArmed:
		if w.k.triggered(w.cfg) {
			if w.updating != nil && w.updating() {
				if now.Sub(w.stillBadLog) >= 10*time.Minute {
					log.Info("wifi watchdog: the Wi-Fi needs a restart, waiting for the running update to finish")
					w.stillBadLog = now
				}
				return
			}
			w.restart(now, win.iface)
		}
	case phaseRestarted:
		switch {
		case w.k.recovered(w.cfg):
			log.Info("wifi watchdog: the Wi-Fi works normally again after its restart; re-armed")
			if !w.st.Alerted {
				w.queueAlert("The Wi-Fi was restarted", "sending data had slowed down to about "+w.st.Slow+"."+w.rebootNote()+" It is working normally again.")
			}
			w.st.Alerted = false
			w.setPhase(phaseArmed)
		case w.k.triggered(w.cfg):
			log.Info("wifi watchdog: still bad after the restart (" + w.k.describe() + "); not restarting it again until it has been healthy for a while")
			w.queueAlert("The Wi-Fi is still slow after a restart",
				"sending data is still slow (about "+w.k.describe()+"). The Wi-Fi won't be restarted again until it has worked normally for a while. Restart the device from Settings, or connect it to your router with an Ethernet cable.")
			w.setPhase(phaseGaveUp)
		case !w.st.Alerted && now.Sub(w.st.RestartedAt) >= w.cfg.VerdictWait:
			log.Info("wifi watchdog: not enough traffic since the restart to judge it; telling the owner it happened")
			w.st.Alerted = true
			w.queueAlert("The Wi-Fi was restarted", "sending data had slowed down to about "+w.st.Slow+"."+w.rebootNote()+" There hasn't been enough traffic since to tell whether it is working normally again.")
		}
	case phaseGaveUp:
		switch {
		case w.k.recovered(w.cfg):
			log.Info("wifi watchdog: the Wi-Fi has been healthy for", w.cfg.HealthyMinutes, "minutes; re-armed")
			w.setPhase(phaseArmed)
		case w.k.triggered(w.cfg) && now.Sub(w.stillBadLog) >= time.Hour:
			log.Info("wifi watchdog: the Wi-Fi is still bad (" + w.k.describe() + "); already restarted once in this episode, not again")
			w.stillBadLog = now
		}
	}
}

func (w *Watchdog) rebootNote() string {
	if w.st.RootNote == "rebooted" {
		return " It didn't reconnect after the restart, so the device restarted."
	}
	return ""
}

// logWindow logs a minute that matters: every bad one, and every one
// counted while the watchdog is judging a restart or waiting to re-arm.
func (w *Watchdog) logWindow(win window) {
	if win.kind == kindNone || win.kind == kindIdle {
		return
	}
	line := fmt.Sprintf("wifi watchdog: %s minute on %s: %.2f%% of %d segments retransmitted, %s out, %d tx errors/drops",
		map[kind]string{kindBad: "bad", kindHealthy: "healthy", kindMiddling: "middling"}[win.kind],
		win.iface, win.pct, win.segs, rate(win.txRate), win.txErrors)
	if win.staCounted {
		line += fmt.Sprintf(", %d of %d frames unacknowledged", win.staFailed, win.staTx)
	}
	line += fmt.Sprintf(" (streak: %d bad, %d healthy; phase %s)", w.k.bad, w.k.healthy, w.st.Phase)
	// Given up: bad minutes are the expected story, logged once an hour
	// (step), not every minute for as long as the episode lasts.
	if (win.kind == kindBad && w.st.Phase != phaseGaveUp) || (w.st.Phase != phaseArmed && win.kind != kindBad) {
		log.Info(line)
	} else {
		log.Debug(line)
	}
}

// restart asks the root side for a Wi-Fi restart: the once of this
// episode.
func (w *Watchdog) restart(now time.Time, iface string) {
	slow := w.k.describe()
	why := fmt.Sprintf("%d bad minutes", w.k.bad)
	if w.k.corroborated(w.cfg) {
		why += fmt.Sprintf(", the radio reporting failures (%d tx errors/drops, %d of %d frames unacknowledged)", w.k.txErrors, w.k.staFail, w.k.staTx)
	}
	w.st.Iface, w.st.Slow, w.st.RootNote, w.st.Alerted = iface, slow, "", false
	if !w.runnerInstalled() {
		log.Info("wifi watchdog: the Wi-Fi on " + iface + " is stuck (" + slow + "; " + why + "), but this device has no Wi-Fi restart runner (otc-wifi-restart-runner); giving up for this episode")
		w.queueAlert("The Wi-Fi is sending data very slowly",
			"sending data has slowed down to about "+slow+". This device can't restart its Wi-Fi by itself until it is updated. Restart the device from Settings, or connect it to your router with an Ethernet cable.")
		w.setPhase(phaseGaveUp)
		return
	}
	// A previous answer must not be taken for this one's.
	_ = os.Remove(w.statusPath)
	stamp := now.UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(w.requestPath, []byte(stamp), 0o644); err != nil { // perms: rw-r--r--
		log.Error("wifi watchdog: could not ask for the Wi-Fi restart:", err)
		return
	}
	log.Info("wifi watchdog: restarting the Wi-Fi on " + iface + ": " + slow + " (" + why + ")")
	w.st.RequestedAt = now
	w.setPhase(phaseRestarting)
}

// checkRoot reads the root side's answer to the restart.
func (w *Watchdog) checkRoot(now time.Time) {
	st, ok := w.readRootStatus()
	if ok {
		switch st.State {
		case "done", "rebooted":
			log.Info("wifi watchdog: the Wi-Fi was restarted (" + st.State + ": " + st.Message + "); judging it once traffic flows")
			w.st.RootNote = st.State
			w.st.RestartedAt = now
			w.setPhase(phaseRestarted)
			w.prev = nil
			return
		case "failed":
			msg := truncate(st.Message, 200)
			log.Info("wifi watchdog: the Wi-Fi restart failed: " + msg + "; giving up for this episode")
			w.queueAlert("The Wi-Fi could not be restarted",
				"sending data had slowed down to about "+w.st.Slow+", and restarting the Wi-Fi failed ("+msg+"). Restart the device from Settings, or connect it to your router with an Ethernet cable.")
			w.setPhase(phaseGaveUp)
			return
		}
	}
	if now.Before(w.st.RequestedAt) {
		w.st.RequestedAt = now
		w.save()
	}
	if now.Sub(w.st.RequestedAt) >= w.cfg.RootWait {
		log.Info("wifi watchdog: no answer from the Wi-Fi restart runner in", w.cfg.RootWait, "- giving up for this episode")
		_ = os.Remove(w.requestPath)
		w.queueAlert("The Wi-Fi could not be restarted",
			"sending data had slowed down to about "+w.st.Slow+", and the Wi-Fi restart never answered. Restart the device from Settings, or connect it to your router with an Ethernet cable.")
		w.setPhase(phaseGaveUp)
	}
}

// readRootStatus reads the runner's answer, only one written for this
// request (the file was removed before asking; the time is checked too).
func (w *Watchdog) readRootStatus() (rootStatus, bool) {
	raw, err := os.ReadFile(w.statusPath)
	if err != nil {
		return rootStatus{}, false
	}
	var st rootStatus
	if json.Unmarshal(raw, &st) != nil {
		return rootStatus{}, false
	}
	if at, err := time.Parse(time.RFC3339, st.Updated); err == nil && at.Before(w.st.RequestedAt.Add(-time.Minute)) {
		return rootStatus{}, false
	}
	return st, true
}

// queueAlert records the Alert in the state first, so one the database
// refuses (or a restart of otc in between) isn't lost, then raises it.
func (w *Watchdog) queueAlert(title, detail string) {
	w.st.Pending = &PendingAlert{Title: title, Detail: detail}
	w.save()
	w.flushAlert()
}

func (w *Watchdog) flushAlert() {
	if w.st.Pending == nil || w.alert == nil {
		return
	}
	if err := w.alert(w.st.Pending.Title, w.st.Pending.Detail); err != nil {
		log.Error("wifi watchdog: could not add the Alert, retrying in a minute:", err)
		return
	}
	log.Info("wifi watchdog: Alert raised: " + w.st.Pending.Title)
	w.st.Pending = nil
	w.save()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
