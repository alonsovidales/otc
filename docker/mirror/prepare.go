// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// prepare finds, assembles or builds the mirror on the two disks, mounts it
// and adds a replacement disk; it prints what the entrypoint needs to stdout.
// noCreate (restore): assemble and mount only, never create, wipe or add.
// A refusal changes nothing unless its message says so.
func (h *host) prepare(noCreate bool) error {
	// 1. Two distinct block devices.
	if h.disks[0].absent() && h.disks[1].absent() {
		return fmt.Errorf("OTC_MIRROR_DISK1 and OTC_MIRROR_DISK2 are both /dev/null in .env: name the mirror's disks")
	}
	for i := range h.disks {
		d := &h.disks[i]
		if d.absent() {
			continue
		}
		id, err := h.devOf(d.path)
		if err != nil {
			return fmt.Errorf("%s (%s) is not a disk: check OTC_MIRROR_DISK%d in .env", d.label, d.path, d.n)
		}
		d.id = id
	}
	d1, d2 := &h.disks[0], &h.disks[1]
	if d1.id != "" && d1.id == d2.id {
		return fmt.Errorf("OTC_MIRROR_DISK1 and OTC_MIRROR_DISK2 are the same disk (%s): name two different disks in .env", d1.id)
	}
	h.say("disk 1: %s; disk 2: %s", h.seen(d1), h.seen(d2))

	// 2. Our array: running already, assembled from the disks, or new.
	md, how, err := h.ourArray(noCreate)
	if err != nil {
		return err
	}
	node, err := h.node(md)
	if err == nil {
		err = h.use(md, node, how, noCreate)
	}
	switch {
	case err == nil:
	case how == assembled && node != "":
		h.run("mdadm", "--stop", node) // back as it was: stopped
	case how == created:
		err = fmt.Errorf("%v (the new mirror %s on %s and %s stays: the next start goes on from there)", err, md, d1.label, d2.label)
	}
	return err
}

// How the array came to be running.
const (
	adopted   = iota // it was already
	assembled        // from its disks' superblocks, here
	created          // a new mirror, here
)

// use mounts the array and prints the result (steps 3 to 6).
func (h *host) use(md, node string, how int, noCreate bool) error {
	r := h.run("mdadm", "--detail", "--export", node)
	info := kv(r.out)
	if r.code != 0 || info["MD_UUID"] == "" {
		return fmt.Errorf("can't read the mirror %s (mdadm --detail: %s)", md, r.why())
	}
	if h.expect == "" && how != created && !otcName(info["MD_NAME"]) {
		return fmt.Errorf("%s is an array OTC didn't build (it is called %q): OTC won't use it - name other disks in .env", md, info["MD_NAME"])
	}
	if h.expect != "" && info["MD_UUID"] != h.expect {
		return fmt.Errorf("the mirror %s on these disks (%s) isn't this device's (%s): name its disks in .env and run docker compose up -d again. To use this one instead, delete storage-mode in the device's config folder first", md, info["MD_UUID"], h.expect)
	}

	// 3. An ext4 filesystem.
	if err := h.filesystem(md, node, how == created); err != nil {
		return err
	}

	// 4. Not mounted anywhere else, then mounted here.
	if err := h.free(node); err != nil {
		if errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("the mirror %s is mounted somewhere else on this server (the host or another container): unmount it there first", md)
		}
		return fmt.Errorf("can't open the mirror %s (%v): check that docker compose uses compose.mirror.yaml (COMPOSE_FILE in .env)", md, err)
	}
	if r := h.run("mount", "-t", "ext4", "-o", "nosuid,nodev", node, h.mount); r.code != 0 {
		return fmt.Errorf("mounting the mirror %s on %s failed (%s): check that docker compose uses compose.mirror.yaml (its security_opt)", md, h.mount, r.why())
	}

	// 5. A replacement for a missing member.
	if !noCreate {
		h.addMissing(md, node, info["MD_UUID"])
	}

	// 6. What the entrypoint needs, and the state in words.
	d1, d2 := &h.disks[0], &h.disks[1]
	fmt.Fprintf(h.stdout, "array=%s\ndev1=%s\ndev2=%s\nlabel1=%s\nlabel2=%s\nuuid=%s\n",
		md, d1.id, d2.id, d1.label, d2.label, info["MD_UUID"])
	state := "state unknown"
	if a, err := h.readArray(md); err == nil {
		text, pct := h.condition(a)
		state = join(text, pct)
	}
	h.say("the mirror %s on %s and %s: %s", md, d1.label, d2.label, state)
	return nil
}

func join(text, pct string) string {
	if pct == "" {
		return text
	}
	return text + ", " + pct
}

// ourArray is the array on our disks (step 2): one that runs already, one
// assembled from the disks' superblocks, or a new one.
func (h *host) ourArray(noCreate bool) (string, int, error) {
	held, err := h.holding()
	if err != nil {
		return "", 0, err
	}
	if len(held) > 0 {
		md, err := h.adopt(held)
		return md, adopted, err
	}
	// A disk that can't be read is left out when the other holds the mirror
	// (it is what a mirror is for); with nothing on the other, nothing is built.
	var sb [2]map[string]string
	var unreadable []error
	for i := range h.disks {
		if sb[i], err = h.examine(&h.disks[i]); err != nil {
			unreadable = append(unreadable, err)
		}
	}
	if len(unreadable) > 0 {
		if sb[0] == nil && sb[1] == nil {
			for i := range h.disks {
				if d := &h.disks[i]; d.absent() {
					o := &h.disks[1-i]
					return "", 0, fmt.Errorf("OTC_MIRROR_DISK%d is /dev/null in .env, and %s holds no mirror to run on alone: name both disks in .env", d.n, o.label)
				}
			}
			return "", 0, unreadable[0]
		}
		h.say("%v - the mirror is assembled without it", unreadable[0])
	}
	if sb[0] != nil || sb[1] != nil {
		md, err := h.assemble(sb)
		return md, assembled, err
	}
	if noCreate {
		return "", 0, fmt.Errorf("there is no mirror on %s or %s yet, and restore only uses one: check OTC_MIRROR_DISK1 and OTC_MIRROR_DISK2 in .env", h.disks[0].label, h.disks[1].label)
	}
	if h.expect != "" {
		// Its own mirror missing, a device never starts over on a new one
		// by itself: its files would seem gone.
		return "", 0, fmt.Errorf("this device's mirror (%s) isn't on %s or %s: name its disks in .env and run docker compose up -d again. To start over with a new, empty mirror on these disks, delete storage-mode in the device's config folder first", h.expect, h.disks[0].label, h.disks[1].label)
	}
	md, err := h.build()
	return md, created, err
}

// adopt takes over the array that runs already on our disks (2a): a two-disk
// raid1 of nothing but them.
func (h *host) adopt(held []*array) (string, error) {
	a := held[0]
	var d *disk
	for _, m := range a.members {
		if d = h.diskOf(m.id); d != nil {
			break
		}
	}
	if len(held) > 1 {
		return "", fmt.Errorf("%s belongs to %s and the other disk to %s, two arrays on this server: OTC won't touch them - name other disks in .env", d.label, a.name, held[1].name)
	}
	if !a.running() {
		return "", fmt.Errorf("%s belongs to %s, an array on this server that isn't running: stop it on the host (mdadm --stop /dev/%s) and run docker compose up -d again", d.label, a.name, a.name)
	}
	if a.level != "raid1" || a.raidDisks != 2 {
		return "", fmt.Errorf("%s belongs to %s, another array on this server (%s of %d disks): OTC won't touch it - name other disks in .env", d.label, a.name, a.level, a.raidDisks)
	}
	if !h.ours(a) {
		var others []string
		for _, m := range a.members {
			if h.diskOf(m.id) == nil {
				others = append(others, m.name)
			}
		}
		return "", fmt.Errorf("%s belongs to %s, another array on this server (it also holds %s): OTC won't touch it. If %s is this device's mirror and %s a disk you replaced, stop it on the host (mdadm --stop /dev/%s) and run docker compose up -d again",
			d.label, a.name, strings.Join(others, " and "), a.name, strings.Join(others, " and "), a.name)
	}
	h.say("the mirror %s is running already on this server: using it", a.name)
	return a.name, nil
}

// assemble starts the array from the superblocks found on our disks (2b),
// degraded when only one disk has one.
func (h *host) assemble(sb [2]map[string]string) (string, error) {
	var with []*disk
	for i := range h.disks {
		d := &h.disks[i]
		if sb[i] == nil {
			continue
		}
		if sb[i]["MD_LEVEL"] != "raid1" || sb[i]["MD_DEVICES"] != "2" {
			return "", fmt.Errorf("%s holds part of another kind of array (%s of %s disks): OTC won't touch it. If nothing on it is needed, erase it on the host with wipefs -a %s (only if you are sure) and run docker compose up -d again",
				d.label, sb[i]["MD_LEVEL"], sb[i]["MD_DEVICES"], d.hostPath())
		}
		switch {
		case h.expect != "" && sb[i]["MD_UUID"] != h.expect:
			return "", fmt.Errorf("%s holds part of another mirror (%s), not this device's (%s): name this device's disks in .env and run docker compose up -d again. To use that one instead, delete storage-mode in the device's config folder first",
				d.label, sb[i]["MD_UUID"], h.expect)
		case h.expect == "" && !otcName(sb[i]["MD_NAME"]):
			return "", fmt.Errorf("%s holds part of an array OTC didn't build (it is called %q): OTC won't use it. If nothing on it is needed, erase it on the host with wipefs -a %s (only if you are sure) and run docker compose up -d again",
				d.label, sb[i]["MD_NAME"], d.hostPath())
		}
		with = append(with, d)
	}
	if len(with) == 2 && sb[0]["MD_UUID"] != sb[1]["MD_UUID"] {
		return "", fmt.Errorf("%s and %s hold parts of two different arrays: OTC won't mix them. If one of them holds nothing you need, erase it on the host (wipefs -a %s, or wipefs -a %s; only if you are sure) and run docker compose up -d again",
			with[0].label, with[1].label, with[0].hostPath(), with[1].hostPath())
	}
	args := []string{"--assemble", "--run", h.dev + "/md/otc"}
	for _, d := range with {
		if err := h.inUse(d); err != nil {
			return "", err
		}
		args = append(args, d.path)
	}
	if len(with) == 1 {
		h.say("assembling the mirror from %s alone", with[0].label)
	} else {
		h.say("assembling the mirror from %s and %s", with[0].label, with[1].label)
	}
	if r := h.run("mdadm", args...); r.code != 0 {
		return "", fmt.Errorf("assembling the mirror failed (%s): check the disks named in .env", r.why())
	}
	return h.found()
}

// build makes a new mirror on two disks with nothing on them (2c), or with
// something on them that .env says may be erased.
func (h *host) build() (string, error) {
	var wipe []*disk
	var held []string   // what the disks hold, that stops the build
	var line []string   // the disks .env must list for it
	var kernel []string // ...named by a kernel name, which it can't list
	var had []string    // what the disks to erase hold
	for i := range h.disks {
		d := &h.disks[i]
		if err := h.inUse(d); err != nil {
			return "", err
		}
		if err := h.readEnds(d.path); err != nil {
			return "", fmt.Errorf("can't read %s (%v): check that the disk works and is connected", d.label, err)
		}
		f, err := h.signatures(d.path)
		if err != nil {
			return "", err
		}
		switch {
		case f.blank():
			continue
		case f.raidMember():
			return "", fmt.Errorf("%s holds part of a disk array (%s) that OTC can't use: it won't erase it. If nothing on it is needed, erase it on the host with wipefs -a %s (only if you are sure) and run docker compose up -d again",
				d.label, f, d.hostPath())
		case h.erasable(d):
			wipe, had = append(wipe, d), append(had, f.String())
		default:
			held = append(held, fmt.Sprintf("%s holds %s", d.label, f))
			if !byID(d.name) {
				kernel = append(kernel, fmt.Sprintf("OTC_MIRROR_DISK%d", d.n))
			}
		}
		line = append(line, d.hostPath())
	}
	if len(kernel) > 0 {
		return "", fmt.Errorf("%s: OTC erases a disk only when .env lists it in OTC_MIRROR_ERASE by its /dev/disk/by-id/ name, which stays with the disk (a name like /dev/sdc can be another disk after a restart). Put the disk's /dev/disk/by-id/ name in %s and in OTC_MIRROR_ERASE, if nothing on it is needed, and run docker compose up -d again",
			strings.Join(held, "; "), strings.Join(kernel, " and "))
	}
	if len(held) > 0 {
		return "", fmt.Errorf("%s: OTC erases a disk only when .env says so. If nothing on it is needed, put this line in .env and run docker compose up -d again: %s",
			strings.Join(held, "; "), eraseLine(line))
	}
	var erased []string
	for i, d := range wipe {
		h.say("%s holds %s: erasing it, as OTC_MIRROR_ERASE says", d.label, had[i])
		if r := h.run("wipefs", "-a", d.path); r.code != 0 {
			return "", fmt.Errorf("erasing %s failed (%s)%s", d.label, r.why(), done(erased))
		}
		erased = append(erased, d.label)
	}
	d1, d2 := &h.disks[0], &h.disks[1]
	h.say("building a new mirror on %s and %s", d1.label, d2.label)
	r := h.run("mdadm", "--create", h.dev+"/md/otc", "--run", "--level=1", "--raid-devices=2", "--metadata=1.2",
		"--name=otc", "--homehost=any", "--bitmap=internal", d1.path, d2.path)
	if r.code != 0 {
		return "", fmt.Errorf("building the mirror on %s and %s failed (%s)%s", d1.label, d2.label, r.why(), done(erased))
	}
	return h.found()
}

func eraseLine(names []string) string {
	if len(names) == 1 {
		return "OTC_MIRROR_ERASE=" + names[0]
	}
	return `OTC_MIRROR_ERASE="` + strings.Join(names, " ") + `"`
}

func done(erased []string) string {
	if len(erased) == 0 {
		return ""
	}
	return "; " + strings.Join(erased, " and ") + " erased already"
}

// inUse refuses a disk that something on this server holds.
func (h *host) inUse(d *disk) error {
	err := h.free(d.path)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EBUSY):
		return fmt.Errorf("%s is in use on this server (mounted, or part of another array or volume): OTC won't touch it - free it on the host or name another disk in .env", d.label)
	}
	return fmt.Errorf("can't open %s (%v): check OTC_MIRROR_DISK%d in .env", d.label, err, d.n)
}

// found is the array mdadm just assembled or built, known by its members.
func (h *host) found() (string, error) {
	held, err := h.holding()
	if err != nil {
		return "", err
	}
	if len(held) != 1 || !held[0].running() {
		return "", fmt.Errorf("mdadm started the mirror, but no running array on this server holds %s or %s", h.disks[0].label, h.disks[1].label)
	}
	return held[0].name, nil
}

// node is the array's device in the container, made from sysfs when missing
// (mdadm makes it itself, Docker doesn't for an array that was running).
func (h *host) node(md string) (string, error) {
	node := h.dev + "/" + md
	want := h.read("block/" + md + "/dev")
	if want == "" {
		return "", fmt.Errorf("the mirror %s has no device number in %s/block", md, h.sys)
	}
	if id, err := h.devOf(node); err == nil {
		if id != want {
			return "", fmt.Errorf("%s in the container is %s, not the mirror (%s): recreate the container (docker compose up -d --force-recreate)", node, id, want)
		}
		return node, nil
	}
	if err := h.mknod(node, want); err != nil {
		return "", fmt.Errorf("can't make %s for the mirror (%v): check that docker compose uses compose.mirror.yaml (its cap_add and device_cgroup_rules)", node, err)
	}
	return node, nil
}

// filesystem checks the array holds ext4 (step 3). Only a mirror built in
// this run is ever formatted - and always, whatever its disks held before
// showing through. On any other array nothing is formatted: a filesystem
// that can't be read (a read error on a degraded mirror hides it from blkid
// and wipefs alike) is this device's files until someone checks.
func (h *host) filesystem(md, node string, created bool) error {
	if created {
		h.say("making the filesystem on the mirror %s", md)
		if r := h.run("wipefs", "-a", node); r.code != 0 {
			return fmt.Errorf("clearing the new mirror %s failed (%s)", md, r.why())
		}
		if r := h.run("mkfs.ext4", "-q", "-E", "nodiscard", "-L", "otc-storage", node); r.code != 0 {
			return fmt.Errorf("making the filesystem on the mirror %s failed (%s)", md, r.why())
		}
		return nil
	}
	f, err := h.signatures(node)
	if err != nil {
		return err
	}
	if f.blkid["TYPE"] == "ext4" {
		return nil
	}
	if !f.blank() {
		return fmt.Errorf("the mirror %s holds %s, not ext4: OTC won't format it. Name other disks in .env, or, if nothing on it is needed, erase it on the host (%s) and run docker compose up -d again", md, f, h.eraseBoth(md))
	}
	return fmt.Errorf("no filesystem can be read on the mirror %s, and OTC formats only a mirror it has just built. If it holds this device's files, check it on the host (e2fsck -n /dev/%s) - never format it. If it is a new mirror whose first start stopped half-way, erase it on the host (%s) and run docker compose up -d again", md, md, h.eraseBoth(md))
}

// eraseBoth is how to erase the mirror on the host, its disks by their names
// in .env (wiping /dev/mdN alone would leave the array).
func (h *host) eraseBoth(md string) string {
	var cmds []string
	for i := range h.disks {
		if d := &h.disks[i]; !d.absent() {
			cmds = append(cmds, "wipefs -a "+d.hostPath())
		}
	}
	return "mdadm --stop /dev/" + md + ", then " + strings.Join(cmds, " and ") + "; only if you are sure"
}

// addMissing adds a named disk that isn't in the degraded mirror (step 5):
// one that was a member, a blank one, or one .env says may be erased. A
// disk of another array never; a failed add leaves the mirror degraded.
func (h *host) addMissing(md, node, uuid string) {
	a, err := h.readArray(md)
	if err != nil || a.degraded == 0 {
		return
	}
	for i := range h.disks {
		d := &h.disks[i]
		if d.absent() || a.member(d.id) != nil {
			continue
		}
		if err := h.inUse(d); err != nil {
			h.say("%s not added to the mirror: %v", d.label, err)
			continue
		}
		sb, err := h.examine(d)
		if err != nil {
			h.say("%s not added to the mirror: %v", d.label, err)
			continue
		}
		switch {
		case sb != nil && sb["MD_UUID"] == uuid: // a member before: mdadm re-adds it
			if apart, err := h.diverged(d, a); err != nil || apart {
				if err == nil {
					err = fmt.Errorf("it ran without the disk the mirror runs on now, so each holds changes the other lacks. Copy off what only %s holds (it is a whole mirror on its own), then erase it on the host with wipefs -a %s and run docker compose up -d again", d.label, d.hostPath())
				}
				h.say("%s not added to the mirror: %v", d.label, err)
				continue
			}
		case sb != nil:
			h.say("%s not added to the mirror: it holds part of another array. If nothing on it is needed, erase it on the host with wipefs -a %s (only if you are sure) and run docker compose up -d again", d.label, d.hostPath())
			continue
		default:
			if err := h.readEnds(d.path); err != nil {
				h.say("%s not added to the mirror: it can't be read (%v)", d.label, err)
				continue
			}
			f, err := h.signatures(d.path)
			if err != nil {
				h.say("%s not added to the mirror: %v", d.label, err)
				continue
			}
			if !f.blank() {
				if f.raidMember() {
					h.say("%s not added to the mirror: it holds part of a disk array (%s) that OTC can't use. If nothing on it is needed, erase it on the host with wipefs -a %s (only if you are sure) and run docker compose up -d again", d.label, f, d.hostPath())
					continue
				}
				if !h.erasable(d) {
					if !byID(d.name) {
						h.say("%s not added to the mirror: it holds %s. OTC erases a disk only when .env lists it in OTC_MIRROR_ERASE by its /dev/disk/by-id/ name: put that name in OTC_MIRROR_DISK%d and in OTC_MIRROR_ERASE, if nothing on it is needed, and run docker compose up -d again", d.label, f, d.n)
					} else {
						h.say("%s not added to the mirror: it holds %s. If nothing on it is needed, put this line in .env and run docker compose up -d again: %s", d.label, f, eraseLine([]string{d.hostPath()}))
					}
					continue
				}
				h.say("%s holds %s: erasing it, as OTC_MIRROR_ERASE says", d.label, f)
				if r := h.run("wipefs", "-a", d.path); r.code != 0 {
					h.say("erasing %s failed (%s): not added to the mirror", d.label, r.why())
					continue
				}
			}
		}
		h.say("adding %s to the mirror %s: everything is copied onto it", d.label, md)
		if r := h.run("mdadm", "--add", node, d.path); r.code != 0 {
			h.say("adding %s to the mirror %s failed (%s): it stays degraded", d.label, md, r.why())
		}
	}
}

// arrayState is mdadm --examine's "Array State : AA" ('A' active, '.'
// missing, 'R' replacing), what the disk's superblock last knew.
var arrayState = regexp.MustCompile(`(?m)^\s*Array State : ([A-Za-z.]+)`)

// diverged: the disk's own superblock says it last ran without a disk the
// mirror runs on now. The two copies then went separate ways (each ran on
// its own, one after the other: /dev/null in .env for one, then the other),
// and adding it would overwrite what only it holds. A disk that dropped out
// while the other went on still shows both as active: it is only behind.
func (h *host) diverged(d *disk, a *array) (bool, error) {
	r := h.run("mdadm", "--examine", d.path)
	if r.code != 0 {
		return false, fmt.Errorf("can't read its superblock (mdadm --examine: %s)", r.why())
	}
	m := arrayState.FindStringSubmatch(r.out)
	if m == nil {
		return false, fmt.Errorf("its superblock shows no array state")
	}
	state := m[1]
	for _, mem := range a.members {
		if mem.slot < 0 || !mem.has("in_sync") {
			continue
		}
		if mem.slot >= len(state) || state[mem.slot] == '.' {
			return true, nil
		}
	}
	return false, nil
}
