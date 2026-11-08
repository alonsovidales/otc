// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"sync"
	"time"

	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/log"
)

// Processing waits for memory. The lanes' worker counts (lanes.go) were
// measured on a Raspberry Pi doing nothing else, but an 8 GB one that also
// shows the web app on a screen, or runs other users' instances, has
// gigabytes less for them: on 2026-11-08 one had ~2.5 GB in use besides
// the device, which held up to 6 GB while a phone uploaded, and the
// kernel killed it every few hours with under 300 MB left. So a job that
// would run beside others starts only while the device has cJobHeadroom
// available for it, counting what the jobs let in during the last
// cAdmitSettle take once their memory shows (a job's decode and model run
// come a second or two after it starts); otherwise it waits for one of
// them to end, or for the memory, and looks again every cAdmitPoll. A job
// with nothing else running always starts: processing gets slower when
// memory is short, it never stops.

// cJobHeadroom is what one job may take at its peak: a 48 MP photo's
// decode and thumbnails (~400 MB), a run of the tagging model (~650 MB)
// or a 4K video frame's ffmpeg (~370 MB), with room left for the uploads
// arriving meanwhile and the rest of the device.
const cJobHeadroom = 1536 << 20

// cAdmitSettle: a job let in counts as taking cJobHeadroom for this long,
// until what it takes shows in the available memory.
const cAdmitSettle = 2 * time.Second

// cAdmitPoll is how often a job waiting for memory looks again.
const cAdmitPoll = time.Second

// cAdmitLogEvery: a wait for memory is logged at most this often.
const cAdmitLogEvery = 10 * time.Minute

// memAdmission lets processing jobs start (memadmit.go's top). Its zero
// value reads the device's memory and the clock.
type memAdmission struct {
	mu      sync.Mutex
	running int
	// recent are when the jobs let in during the last cAdmitSettle were.
	recent []time.Time
	logged time.Time
	high   memoryHighWater

	// available, now and sleep are deviceAvailable, time.Now and
	// time.Sleep, replaced by tests.
	available func() int64
	now       func() time.Time
	sleep     func(time.Duration)
}

// admit waits until a job may start and counts it as running. ok is false,
// with nothing counted, when stop (nil: never) called it off meanwhile.
func (a *memAdmission) admit(stop func() bool) (ok bool) {
	a.mu.Lock()
	for a.running > 0 {
		now := a.clock()
		avail := a.availableNow()
		if avail < 0 {
			break // not known: as before
		}
		pending := a.settling(now)
		if avail-int64(pending)*cJobHeadroom >= cJobHeadroom {
			break
		}
		if avail < cJobHeadroom && now.Sub(a.logged) >= cAdmitLogEvery {
			a.logged = now
			log.Info(fmt.Sprintf("processing: %d MB available, under the %d MB a job needs beside the %d running - the next one waits for them or for memory", avail>>20, cJobHeadroom>>20, a.running)) // i18n-ignore: a log line
		}
		a.mu.Unlock()
		if stop != nil && stop() {
			return false
		}
		a.pause(cAdmitPoll)
		a.mu.Lock()
	}
	a.running++
	a.recent = append(a.recent, a.clock())
	a.mu.Unlock()
	return true
}

// done ends a job admit let start, and logs the process's memory when it
// reached a new high.
func (a *memAdmission) done() {
	a.mu.Lock()
	a.running--
	a.mu.Unlock()
	a.high.check()
}

// settling drops the admissions older than cAdmitSettle and counts the
// rest. Called with mu held.
func (a *memAdmission) settling(now time.Time) int {
	keep := a.recent[:0]
	for _, t := range a.recent {
		if now.Sub(t) < cAdmitSettle {
			keep = append(keep, t)
		}
	}
	a.recent = keep
	return len(keep)
}

func (a *memAdmission) availableNow() int64 {
	if a.available != nil {
		return a.available()
	}
	return deviceAvailable()
}

func (a *memAdmission) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *memAdmission) pause(d time.Duration) {
	if a.sleep != nil {
		a.sleep(d)
		return
	}
	time.Sleep(d)
}

// deviceAvailable is the memory the device can still use, in bytes:
// /proc/meminfo's MemAvailable and, in a container with a memory limit, no
// more than what is left under it (container.MemoryAvailable). -1 when
// neither can be read, as off Linux.
func deviceAvailable() int64 {
	avail := int64(-1)
	if kb := procStatusKB("/proc/meminfo", "MemAvailable"); kb > 0 {
		avail = kb << 10
	}
	if container.Enabled() {
		if left := container.MemoryAvailable(); left >= 0 && (avail < 0 || left < avail) {
			avail = left
		}
	}
	return avail
}
