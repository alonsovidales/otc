// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	lookEvery     = 5 * time.Second
	progressEvery = 10 * time.Minute // a copy's progress in the log
	growEvery     = 10 * time.Minute // bigger disks looked for
	growRetry     = 10 * time.Minute // after a failed step, doubling...
	growRetryMax  = time.Hour        // ...up to this
	growMargin    = 256 << 20        // less room than this is not worth a grow
)

// watcher logs the mirror's state as it changes and grows it onto bigger
// disks, as scripts/raid_watch.py does on a Raspberry Pi. It never adds or
// removes a member: a disk that failed is added back by prepare at the
// container's next start.
type watcher struct {
	h  *host
	md string

	started   bool
	text      string            // the state last logged, its progress apart
	said      time.Time         // when the progress was last logged
	members   map[string]seenAs // by major:minor
	nextCheck time.Time
	grow      backoff
	resize    backoff
}

type seenAs struct {
	label  string
	faulty bool
}

// backoff holds a failed step off: 10 minutes, then 20, 40, an hour.
type backoff struct {
	at    time.Time
	delay time.Duration
}

func (b *backoff) failed(now time.Time) {
	b.delay = min(max(growRetry, 2*b.delay), growRetryMax)
	b.at = now.Add(b.delay)
}

func (b *backoff) ready(now time.Time) bool { return !now.Before(b.at) }

// watch looks at the array every 5 s until the process is stopped.
func (h *host) watch(md string) {
	h.say("the kernel's own messages about the disks are not in this log: a container can't read them (on the host: journalctl -k)")
	h.lookUp()
	w := &watcher{h: h, md: md}
	for {
		w.step(time.Now())
		time.Sleep(lookEvery)
	}
}

// step is one look at the array.
func (w *watcher) step(now time.Time) {
	h := w.h
	a, err := h.readArray(w.md)
	if err != nil {
		w.note(now, "not running on this server any more", "")
		w.members = nil
		return
	}
	members := map[string]seenAs{}
	for _, m := range a.members {
		s := seenAs{label: h.labelOf(m), faulty: m.has("faulty")}
		members[m.id] = s
		if was, ok := w.members[m.id]; w.started && s.faulty && (!ok || !was.faulty) {
			h.say("the mirror %s: %s stopped working (faulty)", w.md, s.label)
		}
	}
	for id, was := range w.members {
		if _, ok := members[id]; !ok && !was.faulty {
			h.say("the mirror %s: %s stopped working (gone)", w.md, was.label)
		}
	}
	w.members, w.started = members, true
	text, pct := h.condition(a)
	w.note(now, text, pct)

	if a.degraded == 0 && a.syncAction == "idle" && !now.Before(w.nextCheck) {
		w.nextCheck = now.Add(growEvery)
		w.growStep(now, a)
	}
}

// note logs the state when it changed, and a copy's progress every 10 min.
func (w *watcher) note(now time.Time, text, pct string) {
	if text != w.text || pct != "" && now.Sub(w.said) >= progressEvery {
		w.h.say("the mirror %s: %s", w.md, join(text, pct))
		w.text, w.said = text, now
	}
}

// growStep grows the array once both disks have room to spare, then the
// filesystem once the array is bigger than it, each held off after a failure.
func (w *watcher) growStep(now time.Time, a *array) {
	h := w.h
	node := h.nodeOf(a)
	if node == "" || !h.ours(a) {
		return
	}
	if w.grow.ready(now) && h.canGrow(a) {
		h.say("the mirror %s: both disks have room to spare, growing it", a.name)
		if r := h.growArray(node); r.code != 0 {
			w.grow.failed(now)
			h.say("growing the mirror %s failed (%s): tried again in %d minutes", a.name, r.why(), int(w.grow.delay.Minutes()))
		} else {
			w.grow = backoff{}
			h.say("the mirror %s grown: the new space is being mirrored", a.name)
		}
	}
	if w.resize.ready(now) && h.fsSmaller(node) {
		h.say("growing the filesystem on the mirror %s", a.name)
		if r := h.run("resize2fs", node); r.code != 0 {
			w.resize.failed(now)
			h.say("growing the filesystem on the mirror %s failed (%s): tried again in %d minutes", a.name, r.why(), int(w.resize.delay.Minutes()))
		} else {
			w.resize = backoff{}
			h.say("the filesystem on the mirror %s grown", a.name)
		}
	}
}

// canGrow: both of our disks are up-to-date members, and each has more room
// than the array uses of it (its size less md's data offset).
func (h *host) canGrow(a *array) bool {
	if a.degraded != 0 || a.syncAction != "idle" || a.raidDisks != 2 || len(a.members) != 2 || !h.ours(a) ||
		a.members[0].id == a.members[1].id || a.componentKiB <= 0 {
		return false
	}
	for _, m := range a.members {
		size := h.readInt("class/block/" + m.name + "/size")
		if !m.has("in_sync") || m.has("faulty") || size < 0 || m.offset < 0 {
			return false
		}
		if (size-m.offset)*512-a.componentKiB*1024 <= growMargin {
			return false
		}
	}
	return true
}

// growArray is mdadm --grow --size=max; an mdadm that refuses with an
// internal bitmap gets it dropped for the grow and put back.
func (h *host) growArray(node string) result {
	r := h.mdadmGrow(node, "--size=max")
	if r.code != 0 && strings.Contains(strings.ToLower(r.out+r.err), "bitmap") {
		h.mdadmGrow(node, "--bitmap=none")
		r = h.mdadmGrow(node, "--size=max")
		if b := h.mdadmGrow(node, "--bitmap=internal"); b.code != 0 {
			h.say("the write-intent bitmap of %s couldn't be put back (%s): a disk added back is copied in full", node, b.why())
		}
	}
	return r
}

// mdadmGrow is mdadm --grow, which sets the new size (and the bitmap)
// through sysfs: Docker mounts /sys read-only in a container ("Cannot set
// device size ...: Read-only file system", seen in a VM), so it runs in a
// mount namespace of its own, on a sysfs of its own mounted read-write -
// the container's /sys stays read-only for everything else.
func (h *host) mdadmGrow(node, what string) result {
	return h.run("unshare", "--mount", "--propagation", "private", "--", "sh", "-c",
		`mount -t sysfs sysfs /sys && exec mdadm --grow "$@"`, "mdadm-grow", node, what)
}

var (
	blockCount = regexp.MustCompile(`(?m)^Block count:\s*(\d+)`)
	blockSize  = regexp.MustCompile(`(?m)^Block size:\s*(\d+)`)
)

// fsSmaller: the ext4 on the array is smaller than the array.
func (h *host) fsSmaller(node string) bool {
	dev, err := strconv.ParseInt(strings.TrimSpace(h.run("blockdev", "--getsize64", node).out), 10, 64)
	if err != nil {
		return false
	}
	out := h.run("dumpe2fs", "-h", node).out
	count, size := blockCount.FindStringSubmatch(out), blockSize.FindStringSubmatch(out)
	if count == nil || size == nil {
		return false
	}
	c, _ := strconv.ParseInt(count[1], 10, 64)
	s, _ := strconv.ParseInt(size[1], 10, 64)
	return dev-c*s > growMargin
}

// release unmounts the mirror and stops it, once the device and MariaDB
// have stopped; each step best effort, said when it fails: a mirror left
// running is used as it is at the next start.
func (h *host) release(md string) {
	h.sync()
	a, err := h.readArray(md)
	if err != nil {
		h.say("the mirror %s is not running: nothing to stop", md)
		return
	}
	if h.mounted(a.id) {
		if r := h.run("umount", h.mount); r.code != 0 {
			h.say("unmounting the mirror %s failed (%s)", md, r.why())
		}
	} else {
		h.say("the mirror %s is not mounted on %s", md, h.mount)
	}
	h.lookUp()
	if !h.ours(a) {
		h.say("%s holds disks other than %s and %s: left running", md, h.disks[0].label, h.disks[1].label)
		return
	}
	node := h.nodeOf(a)
	if node == "" {
		h.say("%s/%s is not the mirror's device: the mirror %s is left running", h.dev, md, md)
		return
	}
	if r := h.run("mdadm", "--stop", node); r.code != 0 {
		h.say("the mirror %s is still in use, left running (%s): the next start uses it as it is", md, r.why())
		return
	}
	h.say("the mirror %s stopped", md)
}

// nodeOf is the array's device node in the container, "" when that isn't it.
func (h *host) nodeOf(a *array) string {
	node := h.dev + "/" + a.name
	if id, err := h.devOf(node); err != nil || a.id == "" || id != a.id {
		return ""
	}
	return node
}

// lookUp finds our disks' major:minor, to tell them among the members; a
// disk that can't be found is named by the kernel's name instead.
func (h *host) lookUp() {
	for i := range h.disks {
		h.disks[i].id, _ = h.devOf(h.disks[i].path)
	}
}

// mounted: the device with that major:minor is what is mounted on the mount
// point (/proc/self/mountinfo), not the empty folder under it.
func (h *host) mounted(id string) bool {
	b, err := os.ReadFile(filepath.Join(h.proc, "self/mountinfo"))
	if err != nil || id == "" {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 && f[2] == id && f[4] == h.mount {
			return true
		}
	}
	return false
}
