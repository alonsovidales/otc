// SPDX-License-Identifier: AGPL-3.0-or-later

package devicerestart

import (
	"os"
	"time"

	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// Settings > Shut down device (issue #202, release 164): the owner of the
// primary instance powers the whole machine off cleanly, so it can be
// unplugged without risking the SD card - a Raspberry Pi has no power
// button, and pulling the plug on one that is running is how cards get
// damaged. The same shape as Restart: the device drops
// /var/lib/otc/poweroff.request, and systemd's otc-poweroff.path starts
// otc-poweroff.service, which runs
// scripts/device-runner/otc-poweroff-runner.sh as root. That removes the
// request, drops it in the first two minutes after boot (a request left
// behind must never switch a device off as it starts: it could never stay
// on) or while an update or a Wi-Fi change runs, waits a moment for the
// answer to leave, and runs `systemctl poweroff`, which stops the service
// and MariaDB cleanly first. The request carries nothing the root side
// reads. The device then stays off until its power is unplugged and
// plugged in again.

const (
	cPowerOffRequestPath = "/var/lib/otc/poweroff.request"
	cPowerOffRunner      = "/usr/local/bin/otc-poweroff-runner"
)

// ErrShutDownNotInstalled is a device whose root side predates the button
// (a hand-installed one that hasn't run release 164's update.sh).
var ErrShutDownNotInstalled = devtext.New(i18n.DevDeviceShutdownNeedsUpdate())

// Seams for tests.
var (
	powerOffRequestPath     = cPowerOffRequestPath
	powerOffRunnerInstalled = func() bool { _, err := os.Stat(cPowerOffRunner); return err == nil }
)

// ShutDown asks the root side to power the device off. It returns once the
// request is written; the shutdown begins a few seconds later, which is
// what lets the answer reach the app first. The caller checks that no
// update or Wi-Fi change runs (websocket), as the root side does too.
func ShutDown() error {
	if !powerOffRunnerInstalled() {
		return ErrShutDownNotInstalled
	}
	// The root side drops a request this soon after boot, so it is refused
	// here first, with words the owner can act on.
	if up, err := uptime(); err == nil && up < cMinUptime {
		return devtext.New(i18n.DevDeviceShutdownTooSoon())
	}
	stamp := time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(powerOffRequestPath, []byte(stamp), 0o644); err != nil { // perms: rw-r--r--
		return devtext.Wrap(i18n.DevDeviceShutdownRequestFailed(err.Error()), err)
	}
	log.Info("device shutdown requested by the owner")
	return nil
}
