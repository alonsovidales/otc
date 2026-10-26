// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// host is everything otc-mirror reads, runs and writes, each part
// replaceable so the tests can fake the disks, sysfs and the commands.
type host struct {
	sys, dev, proc string // /sys, /dev, /proc
	mount          string // where the mirror is mounted: /mnt/storage
	disks          [2]disk
	erase          []string // OTC_MIRROR_ERASE: the names of the disks that may be erased
	expect         string   // OTC_MIRROR_UUID: this device's mirror, once it has one (config/storage-mode)

	run   func(name string, args ...string) result
	devOf func(path string) (string, error) // a block device node's "major:minor"
	free  func(path string) error           // nil when an exclusive open succeeds
	// readEnds reads a disk's first and last MiB: a disk that can't be read
	// is never taken for blank (blkid and wipefs report nothing then).
	readEnds func(path string) error
	mknod    func(path, id string) error // a block device node for "major:minor"
	sync     func()

	log            *storageLog
	stdout, stderr io.Writer
}

// disk is one of the two disks named in .env.
type disk struct {
	n     int    // 1 or 2
	path  string // the node in the container: /dev/otc-disk1
	name  string // OTC_MIRROR_DISK1 as written in .env (a path on the host)
	label string // its basename, for messages and alerts; "disk 1" without one
	id    string // major:minor, once looked up
}

// absent is a disk .env names as /dev/null: none for now (a disk that died
// and is gone from the server, until its replacement is named), so the
// mirror runs on the other one alone. Never one to build a mirror with.
func (d *disk) absent() bool { return strings.TrimSpace(d.name) == "/dev/null" }

// result is what a command did; code is -1 when it couldn't be run.
type result struct {
	out, err string
	code     int
}

// why is the command's own complaint, for a message.
func (r result) why() string {
	if s := strings.TrimSpace(r.err); s != "" {
		return s
	}
	if s := strings.TrimSpace(r.out); s != "" {
		return s
	}
	return fmt.Sprintf("exit status %d", r.code)
}

// say writes a line to the storage log and to stderr, last: the entrypoint
// stops with a refusal's line, the last one prepare writes.
func (h *host) say(format string, args ...any) {
	s := strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, fmt.Sprintf(format, args...))
	h.log.write(s)
	fmt.Fprintln(h.stderr, s)
}

// diskOf is our disk with that major:minor, or nil.
func (h *host) diskOf(id string) *disk {
	for i := range h.disks {
		if id != "" && h.disks[i].id == id {
			return &h.disks[i]
		}
	}
	return nil
}

// erasable: .env lists the disk in OTC_MIRROR_ERASE, by its exact name.
func (h *host) erasable(d *disk) bool {
	return byID(d.name) && slices.Contains(h.erase, d.name)
}

// byID: a /dev/disk/by-id/ name, which stays with the disk. A kernel name
// (/dev/sdc) can be another disk after a restart, so OTC_MIRROR_ERASE never
// takes one: a line left in .env would erase whichever disk has it then.
func byID(name string) bool { return strings.HasPrefix(name, "/dev/disk/by-id/") }

// eraseRefused: .env lists the disk in OTC_MIRROR_ERASE, but by a name that
// doesn't stay with the disk.
func (h *host) eraseRefused(d *disk) bool {
	return !byID(d.name) && slices.Contains(h.erase, d.name)
}

// otcName: an array OTC built (mdadm --name=otc, so "any:otc" or "otc").
// Any other array - another system's mirror, a NAS's - is never used.
func otcName(name string) bool { return name == "otc" || strings.HasSuffix(name, ":otc") }

// hostPath is how the disk is called on the host, for a command to run there.
func (d *disk) hostPath() string {
	if d.name != "" {
		return d.name
	}
	return d.label
}

// labelOf names a disk after the last part of its name in .env.
func labelOf(name string, n int) string {
	name = strings.TrimRight(strings.TrimSpace(name), "/")
	if name == "" || name == "/dev/null" {
		return fmt.Sprintf("disk %d", n)
	}
	return strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return '_'
		}
		return r
	}, filepath.Base(name))
}

// ---------------------------------------------------------------------------
// sysfs: /sys/block/<md>/md, read by major:minor, never by name
// ---------------------------------------------------------------------------

var mdName = regexp.MustCompile(`^md[A-Za-z0-9_]+$`)

type array struct {
	name                  string // md127
	id                    string // its major:minor
	state, level          string // array_state, level
	raidDisks, degraded   int
	syncAction, completed string // sync_action, sync_completed
	componentKiB          int64  // what the array uses of each member
	members               []member
}

type member struct {
	name, id, state string // sdb, 8:16, "in_sync"
	offset          int64  // where md's data starts, in sectors (-1 unknown)
	slot            int    // its place in the array, -1 for none
}

func (m member) has(flag string) bool {
	return slices.Contains(strings.Split(m.state, ","), flag)
}

// running: the kernel runs the array (an inactive one is only gathering
// members, and holds them).
func (a *array) running() bool {
	return a.state != "" && a.state != "clear" && a.state != "inactive"
}

func (a *array) member(id string) *member {
	for i := range a.members {
		if a.members[i].id == id {
			return &a.members[i]
		}
	}
	return nil
}

func (h *host) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(h.sys, rel))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (h *host) readInt(rel string) int64 {
	n, err := strconv.ParseInt(h.read(rel), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func (h *host) readArray(name string) (*array, error) {
	dir := filepath.Join(h.sys, "block", name, "md")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	md := "block/" + name + "/md/"
	a := &array{
		name:         name,
		id:           h.read("block/" + name + "/dev"),
		state:        h.read(md + "array_state"),
		level:        h.read(md + "level"),
		raidDisks:    int(h.readInt(md + "raid_disks")),
		degraded:     int(h.readInt(md + "degraded")),
		syncAction:   h.read(md + "sync_action"),
		completed:    h.read(md + "sync_completed"),
		componentKiB: h.readInt(md + "component_size"),
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "dev-") {
			continue
		}
		p := md + e.Name() + "/"
		slot, err := strconv.Atoi(h.read(p + "slot"))
		if err != nil {
			slot = -1 // "none": faulty, or a spare
		}
		a.members = append(a.members, member{
			name:   strings.TrimPrefix(e.Name(), "dev-"),
			id:     h.read(p + "block/dev"),
			state:  h.read(p + "state"),
			offset: h.readInt(p + "offset"),
			slot:   slot,
		})
	}
	return a, nil
}

// holding is every array on this server (any namespace: sysfs shows the
// host's) that holds one of our disks, running or not.
func (h *host) holding() ([]*array, error) {
	ents, err := os.ReadDir(filepath.Join(h.sys, "block"))
	if err != nil {
		return nil, fmt.Errorf("can't read %s/block: %v", h.sys, err)
	}
	var held []*array
	for _, e := range ents {
		if !mdName.MatchString(e.Name()) {
			continue
		}
		a, err := h.readArray(e.Name())
		if err != nil {
			continue
		}
		if slices.ContainsFunc(a.members, func(m member) bool { return h.diskOf(m.id) != nil }) {
			held = append(held, a)
		}
	}
	return held, nil
}

// ours: every member of the array is one of our disks.
func (h *host) ours(a *array) bool {
	n := 0
	for _, m := range a.members {
		if m.has("faulty") {
			continue // a disk that failed (gone, or replaced in .env since): out of use
		}
		if h.diskOf(m.id) == nil {
			return false
		}
		n++
	}
	return n > 0
}

// labelOf names a member: ours by their .env name, any other by the kernel's.
func (h *host) labelOf(m member) string {
	if d := h.diskOf(m.id); d != nil {
		return d.label
	}
	return m.name
}

// condition is the array's state in words, and a copy's progress apart
// ("3%", or "" when nothing is being copied), so a change of state is told
// from a rebuild moving on.
func (h *host) condition(a *array) (string, string) {
	var working, copying []string
	for _, m := range a.members {
		switch {
		case m.has("faulty"):
		case m.has("in_sync"):
			working = append(working, h.labelOf(m))
		default:
			copying = append(copying, h.labelOf(m))
		}
	}
	pct := percent(a.completed)
	switch {
	case a.syncAction == "recover" && len(copying) > 0:
		return "rebuilding onto " + strings.Join(copying, " and "), pct
	case a.degraded > 0 && len(working) == 1:
		return "degraded, only " + working[0] + " works", ""
	case a.degraded > 0 && len(working) == 0:
		return "degraded, no disk works", ""
	case a.degraded > 0:
		return "degraded", ""
	case a.syncAction == "resync":
		return "resyncing the two disks", pct
	case a.syncAction == "check" || a.syncAction == "repair":
		return "checking the two disks", pct
	case a.syncAction == "reshape":
		return "reshaping", pct
	}
	return "both disks in sync", ""
}

// percent reads sync_completed ("1234 / 5678" sectors, or "none").
func percent(completed string) string {
	done, total, ok := strings.Cut(completed, "/")
	d, err1 := strconv.ParseInt(strings.TrimSpace(done), 10, 64)
	t, err2 := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	if !ok || err1 != nil || err2 != nil || t <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", d*100/t)
}

// seen says which disk the kernel sees behind a name in .env.
func (h *host) seen(d *disk) string {
	if d.absent() {
		return "none (/dev/null in .env)"
	}
	name := d.id
	if l, err := os.Readlink(filepath.Join(h.sys, "dev/block", d.id)); err == nil {
		name = filepath.Base(l) + ", " + d.id
	}
	if n := h.readInt("dev/block/" + d.id + "/size"); n > 0 {
		name += ", " + humanSize(n*512)
	}
	return fmt.Sprintf("%s (%s)", d.label, name)
}

func humanSize(n int64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	}
	return fmt.Sprintf("%.0f MB", float64(n)/1e6)
}

// ---------------------------------------------------------------------------
// What is written on a device
// ---------------------------------------------------------------------------

// kv reads KEY=VALUE lines (mdadm --export, blkid -o export).
func kv(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			m[k] = v
		}
	}
	return m
}

// examine is the md superblock on a disk (mdadm --examine --export), or nil
// when it has none.
func (h *host) examine(d *disk) (map[string]string, error) {
	if d.absent() {
		return nil, fmt.Errorf("OTC_MIRROR_DISK%d is /dev/null in .env (no disk)", d.n)
	}
	r := h.run("mdadm", "--examine", "--export", d.path)
	if r.code == 0 {
		if sb := kv(r.out); sb["MD_UUID"] != "" {
			return sb, nil
		}
		return nil, nil // a partition table, say: no array
	}
	if strings.Contains(r.err+r.out, "No md superblock") {
		return nil, nil
	}
	return nil, fmt.Errorf("can't read %s (mdadm --examine: %s): check that the disk works and is connected", d.label, r.why())
}

// found is what blkid -p and wipefs see on a device.
type found struct {
	blkid  map[string]string // TYPE, LABEL, PTTYPE...
	wipefs string            // its list of signatures
}

// blank: no signature at all.
func (f found) blank() bool {
	return len(f.blkid) == 0 && f.wipefs == ""
}

// raidMember: some disk array's superblock, which is never erased.
func (f found) raidMember() bool {
	return strings.HasSuffix(f.blkid["TYPE"], "_raid_member")
}

func (f found) String() string {
	var parts []string
	if t := f.blkid["PTTYPE"]; t != "" {
		parts = append(parts, "a "+t+" partition table")
	}
	if t := f.blkid["TYPE"]; t != "" {
		s := t
		if l := f.blkid["LABEL"]; l != "" {
			s += fmt.Sprintf(" labelled %q", l)
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 && len(f.blkid) > 0 {
		parts = append(parts, "signatures blkid can't tell apart")
	}
	if len(parts) == 0 {
		line, _, _ := strings.Cut(f.wipefs, "\n")
		parts = append(parts, fmt.Sprintf("signatures wipefs lists (%s)", strings.Join(strings.Fields(line), " ")))
	}
	return strings.Join(parts, " and ")
}

// signatures looks for anything written on a device: blkid -p (a partition
// table, a filesystem, LVM, LUKS, a disk array...) and wipefs's list.
func (h *host) signatures(path string) (found, error) {
	var f found
	switch r := h.run("blkid", "-p", "-o", "export", path); r.code {
	case 0:
		f.blkid = kv(r.out)
		delete(f.blkid, "DEVNAME")
		if len(f.blkid) == 0 {
			f.blkid["?"] = r.out
		}
	case 2: // nothing found
	case 8: // several signatures
		f.blkid = map[string]string{"?": "ambivalent"}
	default:
		return f, fmt.Errorf("can't read %s (blkid: %s)", path, r.why())
	}
	r := h.run("wipefs", "--no-act", "--noheadings", path)
	if r.code != 0 {
		return f, fmt.Errorf("can't read %s (wipefs: %s)", path, r.why())
	}
	f.wipefs = strings.TrimSpace(r.out)
	return f, nil
}
