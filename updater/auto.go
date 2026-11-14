// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/log"
)

// Automatic updates: unless its owner turned them off (Settings > Device
// version), a device checks every cAutoEvery for a release it is missing
// and installs it as the Update button would. The choice and the watch's
// own record live in <working dir>/.auto-update.json, on the boot disk
// like the Wi-Fi watchdog's: the device's, not one user's library's, and
// there before the storage is.
const (
	autoStateFile = ".auto-update.json"

	// cAutoEvery is how often the device looks for an update to install.
	cAutoEvery = 12 * time.Hour
	// cAutoSettle is how long after its start the watch first looks, and
	// cAutoTick how often it looks at whether a check is due - which is
	// also how soon it tries again after an update, a Wi-Fi change or the
	// switch held it up.
	cAutoSettle = 5 * time.Minute
	cAutoTick   = 10 * time.Minute
	// cAutoRetry is how soon a check that couldn't be made (no network,
	// GitHub out of reach) is made again.
	cAutoRetry = time.Hour
	// cAutoTries is how many times the watch starts the update to one
	// release. A release that keeps failing here (rolled back, no room)
	// is then left to the Update button, until a newer one is out.
	cAutoTries = 3
)

// autoState is the file's content.
type autoState struct {
	V int `json:"v"`
	// Off: the owner turned automatic updates off. Absent, they are on.
	Off bool `json:"off,omitempty"`
	// Checked is when the watch last settled a check (RFC 3339, UTC).
	Checked string `json:"checked,omitempty"`
	// Tried is the release the watch last started an update to, and Tries
	// how many times in a row.
	Tried int `json:"tried,omitempty"`
	Tries int `json:"tries,omitempty"`
}

var autoMu sync.Mutex

// autoStatePath is where the state is kept: a variable for the tests.
var autoStatePath = func() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}

	return filepath.Join(wd, autoStateFile)
}

// readAutoState reads the state; a missing or unreadable file is the
// default (on, never checked).
func readAutoState() autoState {
	st := autoState{V: 1}
	path := autoStatePath()
	if path == "" {
		return st
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return autoState{V: 1}
	}
	st.V = 1

	return st
}

// writeAutoState stores the state: a temporary file, synced, then renamed.
func writeAutoState(st autoState) error {
	path := autoStatePath()
	if path == "" {
		return os.ErrNotExist
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), autoStateFile+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

// rootRunnerThere: the device has the root runner an update is handed to.
// A variable for the tests.
var rootRunnerThere = func() bool {
	_, err := os.Stat(cRootRunner)

	return err == nil
}

// AutoSupported reports whether this device can update itself unasked: not
// a container (its owner pulls the new image), and only with the root
// runner - the old sudo path is a hand-set-up development box's.
func AutoSupported() bool {
	return !container.Enabled() && rootRunnerThere()
}

// AutoEnabled reports whether automatic updates are on: they are unless
// the owner turned them off.
func AutoEnabled() bool {
	autoMu.Lock()
	defer autoMu.Unlock()

	return !readAutoState().Off
}

// SetAuto turns automatic updates on or off. Turning them on forgets the
// tries counted against a release, so the watch has another go at it.
func SetAuto(on bool) error {
	autoMu.Lock()
	defer autoMu.Unlock()
	st := readAutoState()
	if st.Off == !on {
		return nil
	}
	st.Off = !on
	if on {
		st.Tried, st.Tries = 0, 0
	}
	if err := writeAutoState(st); err != nil {
		return err
	}
	log.Info("automatic updates turned on:", on)

	return nil
}

// autoDeps is what a pass of the watch asks of the rest: the tests stand
// in for all of it.
type autoDeps struct {
	now     func() time.Time
	pending func() bool
	check   func() (*Info, error)
	// apply starts the update: ran false when it was held up (a Wi-Fi
	// change under way), to try again soon.
	apply func() (ran bool, err error)
}

// autoDue reports whether a check is due: none settled yet, the last one
// cAutoEvery or more ago, or one dated after now (a device has no clock of
// its own until the network gives it the time).
func autoDue(st autoState, now time.Time) bool {
	if st.Checked == "" {
		return true
	}
	checked, err := time.Parse(time.RFC3339, st.Checked)
	if err != nil || checked.After(now) {
		return true
	}

	return now.Sub(checked) >= cAutoEvery
}

// autoPass is one look of the watch. It returns how long to wait before a
// check that couldn't be made is made again (0: at the next tick's own
// reckoning).
func autoPass(d autoDeps) time.Duration {
	autoMu.Lock()
	st := readAutoState()
	autoMu.Unlock()
	now := d.now()
	if st.Off || !autoDue(st, now) {
		return 0
	}
	// An update asked for from Settings, or still running: its end
	// restarts the service, and the watch starts over.
	if d.pending() {
		return 0
	}
	info, err := d.check()
	if err != nil {
		log.Debug("automatic update: the check failed:", err)
		return cAutoRetry
	}
	if info.ImageCheckFailed {
		return cAutoRetry
	}

	settle := func(change func(*autoState)) {
		autoMu.Lock()
		defer autoMu.Unlock()
		// Read again: the switch may have moved during the check.
		cur := readAutoState()
		cur.Checked = now.UTC().Format(time.RFC3339)
		if change != nil {
			change(&cur)
		}
		if err := writeAutoState(cur); err != nil {
			log.Error("automatic update: could not keep its record:", err)
		}
	}

	if info.VersionUnknown || len(info.Pending) == 0 {
		settle(nil)
		return 0
	}
	target := info.LatestVersion
	if st.Tried == target && st.Tries >= cAutoTries {
		log.Info("automatic update: release", target, "was tried", st.Tries, "times and is left to Settings' Update")
		settle(nil)
		return 0
	}
	// The switch turned off while the check ran: nothing is installed.
	if !AutoEnabled() {
		return 0
	}
	ran, err := d.apply()
	if !ran {
		log.Info("automatic update: held up by a Wi-Fi change, trying again soon")
		return 0
	}
	if err != nil {
		log.Error("automatic update: could not start the update to release", target, ":", err)
		settle(nil)
		return 0
	}
	log.Info("automatic update: installing release", target)
	settle(func(cur *autoState) {
		if cur.Tried == target {
			cur.Tries++
		} else {
			cur.Tried, cur.Tries = target, 1
		}
	})

	return 0
}

// AutoWatch installs new releases by itself, every cAutoEvery, while
// automatic updates are on. apply starts an update as Settings' Update
// does (ran false: held up, see autoDeps). It never returns; a device that
// can't update itself (AutoSupported) does nothing.
func AutoWatch(apply func() (ran bool, err error)) {
	if !AutoSupported() {
		return
	}
	d := autoDeps{now: time.Now, pending: Pending, check: Check, apply: apply}
	time.Sleep(cAutoSettle)
	for {
		if wait := autoPass(d); wait > 0 {
			time.Sleep(wait)
			continue
		}
		time.Sleep(cAutoTick)
	}
}
