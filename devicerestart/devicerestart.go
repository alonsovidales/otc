// SPDX-License-Identifier: AGPL-3.0-or-later

// Package devicerestart backs Settings > Restart device: the owner of the
// primary instance restarts the whole machine.
//
// otc.service runs unprivileged with NoNewPrivileges, so it can't reboot
// anything itself. Like the Update button it drops a trigger file,
// /var/lib/otc/reboot.request, and systemd's otc-reboot.path starts
// otc-reboot.service, which runs scripts/device-runner/otc-reboot-runner.sh
// as root: that removes the request and runs `systemctl reboot`. The
// request carries nothing the root side reads.
package devicerestart

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/log"
)

const (
	cRequestPath = "/var/lib/otc/reboot.request"
	cRootRunner  = "/usr/local/bin/otc-reboot-runner"

	// ExpectedDowntime is about how long a restart keeps the device away,
	// what the apps are told to expect.
	ExpectedDowntime = time.Minute

	// cMinUptime: the root side refuses a restart this soon after boot
	// (a request somehow left behind must never become a restart loop),
	// so it is refused here first, with words the owner can act on.
	cMinUptime = 2 * time.Minute
)

// ErrNotInstalled is a device whose root side predates the button.
var ErrNotInstalled = errors.New("this device can't be restarted from here until it is updated")

// Seams for tests.
var (
	requestPath     = cRequestPath
	runnerInstalled = func() bool { _, err := os.Stat(cRootRunner); return err == nil }
	uptime          = readUptime
)

// Request asks the root side to restart the device. It returns once the
// request is written; the restart begins a few seconds later, which is
// what lets the answer reach the app first.
func Request() error {
	if !runnerInstalled() {
		return ErrNotInstalled
	}
	if up, err := uptime(); err == nil && up < cMinUptime {
		return fmt.Errorf("the device has only just started; try again in a minute")
	}
	stamp := time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(requestPath, []byte(stamp), 0o644); err != nil { // perms: rw-r--r--
		return fmt.Errorf("could not ask for the restart: %w", err)
	}
	log.Info("device restart requested by the owner")
	return nil
}

// readUptime is the time since boot, from /proc/uptime.
func readUptime() (time.Duration, error) {
	f, err := os.Open("/proc/uptime")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return 0, err
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, errors.New("empty /proc/uptime")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(secs * float64(time.Second)), nil
}
