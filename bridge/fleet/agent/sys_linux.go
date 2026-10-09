// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package agent

import (
	"github.com/alonsovidales/otc/bridge/fleet"
	"golang.org/x/sys/unix"
)

// diskUsage is statfs of a mount point: what df shows, Used counted as df
// does (total minus free, reserved blocks included).
func diskUsage(m Mount) (fleet.Disk, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(m.Point, &st); err != nil {
		return fleet.Disk{}, err
	}
	bs := uint64(st.Bsize)
	d := fleet.Disk{
		Mount:      m.Point,
		FS:         m.FS,
		Total:      st.Blocks * bs,
		Avail:      st.Bavail * bs,
		Used:       (st.Blocks - st.Bfree) * bs,
		Inodes:     st.Files,
		InodesFree: st.Ffree,
	}
	return d, nil
}

// staNano is STA_NANO (linux/timex.h): Offset is in nanoseconds.
const staNano = 0x2000

// clock reads the kernel's NTP state without changing it (adjtimex with
// no mode bits needs no privilege): chrony and systemd-timesyncd both
// keep it.
func clock() (*fleet.Clock, error) {
	var tx unix.Timex
	state, err := unix.Adjtimex(&tx)
	if err != nil {
		return nil, err
	}
	div := 1000.0 // microseconds -> ms
	if tx.Status&staNano != 0 {
		div = 1e6
	}
	return &fleet.Clock{
		Synced:     state != unix.TIME_ERROR && tx.Status&unix.STA_UNSYNC == 0,
		OffsetMs:   round1(float64(tx.Offset) / div),
		MaxErrorMs: round1(float64(tx.Maxerror) / 1000), // always microseconds
	}, nil
}
