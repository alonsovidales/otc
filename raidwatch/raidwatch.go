// SPDX-License-Identifier: AGPL-3.0-or-later

// Package raidwatch tells the owner when a disk of the mirror (md0) stops
// working, and which one: an Alert and a push notification naming the USB
// port it is in ("the top blue USB port"), once per failure, and another
// once the mirror is complete again.
//
// A failed USB reader usually vanishes from the system, so where each
// disk is plugged in is recorded while the array is healthy
// (<storage>/.raid-members.json) and looked up when a slot goes missing.
// Everything is read from sysfs, which the unprivileged service may read;
// the root scripts/raid_watch.py still drives the LEDs and re-adds a
// replacement disk. Main instance only (websocket.Init).
package raidwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
)

// sysRoot is a var so tests can point it at a fake tree.
var sysRoot = "/sys"

const (
	mdName       = "md0"
	membersFile  = ".raid-members.json"
	alertedFile  = ".raid-alerted"
	firstCheck   = 30 * time.Second
	checkEvery   = time.Minute
	unknownLabel = "one of the two storage disks"
)

// bluePort is where each of the Raspberry Pi 5's two USB controllers has
// its blue (USB 3) port: the Pi 5 has one controller per blue port, the
// port being the first one of that controller's USB 3 root hub. Which is
// on top is NOT yet confirmed on hardware: the startup log names each
// disk's port ("raid: slot 0 is the disk in the top blue USB port
// (xhci-hcd.1 usb4 4-1)"), so one look at a device whose readers are known
// settles it - swap the two values if it says the opposite.
var bluePort = map[string]string{
	"xhci-hcd.0": "bottom",
	"xhci-hcd.1": "top",
}

// Member is one disk of the array, where it is plugged in, and its state.
type Member struct {
	Slot     int    `json:"slot"`
	Dev      string `json:"dev"`
	State    string `json:"-"`
	Port     string `json:"port"`               // e.g. "xhci-hcd.1 usb4 4-1"
	Position string `json:"position,omitempty"` // "top", "bottom", or "" when not a Pi 5 blue port
	Serial   string `json:"serial,omitempty"`
}

// inSync: md counts the member as an up-to-date copy.
func (m Member) inSync() bool {
	for _, s := range strings.Split(m.State, ",") {
		if s == "in_sync" {
			return true
		}
	}
	return false
}

// Label names the disk the way the owner can find it.
func (m Member) Label() string {
	if m.Position != "" {
		return "the disk in the " + m.Position + " blue USB port"
	}
	return unknownLabel
}

type array struct {
	raidDisks  int
	degraded   bool
	syncAction string
	members    []Member
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readArray reads md0 from sysfs; ok is false when there is no array.
func readArray() (a array, ok bool) {
	md := filepath.Join(sysRoot, "block", mdName, "md")
	n, err := strconv.Atoi(readTrim(filepath.Join(md, "raid_disks")))
	if err != nil || n == 0 {
		return a, false
	}
	a.raidDisks = n
	d := readTrim(filepath.Join(md, "degraded"))
	a.degraded = d != "" && d != "0"
	a.syncAction = readTrim(filepath.Join(md, "sync_action"))
	dirs, _ := filepath.Glob(filepath.Join(md, "dev-*"))
	for _, dir := range dirs {
		slot, err := strconv.Atoi(readTrim(filepath.Join(dir, "slot")))
		if err != nil {
			slot = -1 // "none": a spare, or one md has let go of
		}
		dev := strings.TrimPrefix(filepath.Base(dir), "dev-")
		m := Member{Slot: slot, Dev: dev, State: readTrim(filepath.Join(dir, "state"))}
		m.Port, m.Position, m.Serial = locate(dev)
		a.members = append(a.members, m)
	}
	sort.Slice(a.members, func(i, j int) bool { return a.members[i].Slot < a.members[j].Slot })
	return a, true
}

var (
	reController = regexp.MustCompile(`^xhci-hcd\.\d+$`)
	reBus        = regexp.MustCompile(`^usb(\d+)$`)
	rePort       = regexp.MustCompile(`^(\d+)-([0-9.]+)$`)
)

// locate finds where a block device (a disk or a partition) is plugged
// in, from its sysfs path:
// .../1f00300000.usb/xhci-hcd.1/usb4/4-1/4-1:1.0/host0/.../block/sda[/sda1].
// position is "top"/"bottom" only for a Pi 5 blue port: the controller's
// USB 3 root hub, its first port, no hub in between.
func locate(dev string) (port, position, serial string) {
	real, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "class", "block", dev))
	if err != nil {
		return "", "", ""
	}
	parts := strings.Split(filepath.ToSlash(real), "/")
	for i := 0; i+2 < len(parts); i++ {
		if !reController.MatchString(parts[i]) {
			continue
		}
		bus := reBus.FindStringSubmatch(parts[i+1])
		p := rePort.FindStringSubmatch(parts[i+2])
		if bus == nil || p == nil || p[1] != bus[1] {
			return "", "", ""
		}
		port = parts[i] + " " + parts[i+1] + " " + parts[i+2]
		serial = readTrim(filepath.Join(sysRoot, "bus", "usb", "devices", parts[i+2], "serial"))
		speed, _ := strconv.Atoi(readTrim(filepath.Join(sysRoot, "bus", "usb", "devices", parts[i+1], "speed")))
		if speed >= 5000 && p[2] == "1" {
			position = bluePort[parts[i]]
		}
		return port, position, serial
	}
	return "", "", ""
}

// Notify is what the watcher calls: an Alert (title, detail) and a push
// (title, body).
type Notify struct {
	Alert func(title, detail string)
	Push  func(title, body string)
}

var (
	mu     sync.Mutex
	broken string // the Label of what is missing now, "" when healthy
)

// Missing names the disk the mirror is missing now ("the disk in the top
// blue USB port"), or "" - for the storage line of the status.
func Missing() string {
	mu.Lock()
	defer mu.Unlock()
	return broken
}

// Watch checks the array every minute, from 30 s after start. Blocks.
func Watch(storagePath string, n Notify) {
	time.Sleep(firstCheck)
	for {
		check(storagePath, n)
		time.Sleep(checkEvery)
	}
}

func loadKnown(storagePath string) map[int]Member {
	known := map[int]Member{}
	b, err := os.ReadFile(filepath.Join(storagePath, membersFile))
	if err != nil {
		return known
	}
	var list []Member
	if json.Unmarshal(b, &list) == nil {
		for _, m := range list {
			known[m.Slot] = m
		}
	}
	return known
}

func saveKnown(storagePath string, members []Member) {
	b, err := json.Marshal(members)
	if err != nil {
		return
	}
	tmp := filepath.Join(storagePath, membersFile+".tmp")
	if os.WriteFile(tmp, b, 0o600) != nil || os.Rename(tmp, filepath.Join(storagePath, membersFile)) != nil {
		log.Error("raid: could not record where the disks are plugged in")
		_ = os.Remove(tmp)
	}
}

// missingSlots: the slots without an up-to-date copy, with what is known
// of the disk that was there - from now if md still lists it (faulty),
// else from when the array was last healthy.
func missingSlots(a array, known map[int]Member) []Member {
	bySlot := map[int]Member{}
	for _, m := range a.members {
		if m.Slot >= 0 {
			bySlot[m.Slot] = m
		}
	}
	var out []Member
	for s := 0; s < a.raidDisks; s++ {
		m, here := bySlot[s]
		if here && m.inSync() {
			continue
		}
		// A replacement being rebuilt into the slot is not the disk that
		// failed: name the one recorded there.
		if k, ok := known[s]; ok && (!here || m.Port == "" || strings.Contains(m.State, "spare")) {
			m = k
		} else if !here {
			m = Member{Slot: s}
		}
		out = append(out, m)
	}
	return out
}

func check(storagePath string, n Notify) {
	a, ok := readArray()
	if !ok {
		return
	}
	known := loadKnown(storagePath)
	alertedPath := filepath.Join(storagePath, alertedFile)
	_, alertedErr := os.Stat(alertedPath)
	alerted := alertedErr == nil

	if !a.degraded {
		mu.Lock()
		broken = ""
		mu.Unlock()
		// Healthy and settled: record where each disk is, and say so if a
		// failure was reported.
		if a.syncAction != "" && a.syncAction != "idle" {
			return
		}
		var cur []Member
		for _, m := range a.members {
			if m.Slot >= 0 && m.inSync() {
				cur = append(cur, m)
			}
		}
		if len(cur) == a.raidDisks && changed(known, cur) {
			for _, m := range cur {
				log.Info("raid: slot", m.Slot, "is", m.Label(), "("+m.Port+")")
			}
			saveKnown(storagePath, cur)
		}
		if alerted {
			_ = os.Remove(alertedPath)
			if n.Alert != nil {
				n.Alert("Both storage disks are working again", "The mirror is complete: every file is on both disks again.")
			}
			if n.Push != nil {
				n.Push("Storage disks working again", "The mirror is complete: every file is on both disks again.")
			}
		}
		return
	}

	missing := missingSlots(a, known)
	labels := make([]string, 0, len(missing))
	for _, m := range missing {
		labels = append(labels, m.Label())
	}
	what := unknownLabel
	if len(labels) > 0 {
		what = strings.Join(labels, " and ")
	}
	mu.Lock()
	broken = what
	mu.Unlock()
	if alerted {
		return
	}
	// Recorded first: a crash right after must not alert twice, and one
	// alert per failure is enough.
	if err := os.WriteFile(alertedPath, []byte(what+"\n"), 0o600); err != nil {
		log.Error("raid: could not record the alert:", err)
	}
	title := capitalize(what) + " stopped working"
	detail := "Your files are safe on the other disk, but they are no longer mirrored. Replace it with a card at least as large: the device adds it and copies everything onto it by itself, erasing whatever was on the new card."
	for _, m := range missing {
		if m.Dev != "" || m.Serial != "" || m.Port != "" {
			detail += " (" + strings.TrimSpace(strings.Join([]string{m.Dev, m.Serial, m.Port}, " ")) + ")"
		}
	}
	log.Error("raid:", what, "stopped working - the array is degraded")
	if n.Alert != nil {
		n.Alert(title, detail)
	}
	if n.Push != nil {
		n.Push("Storage disk stopped working", capitalize(what)+" failed. Your files are safe on the other one: replace it soon.")
	}
}

// changed: the recorded disks differ from these (another disk, or the
// same one moved to another port).
func changed(known map[int]Member, cur []Member) bool {
	if len(known) != len(cur) {
		return true
	}
	for _, m := range cur {
		k, ok := known[m.Slot]
		if !ok || k.Port != m.Port || k.Serial != m.Serial || k.Dev != m.Dev || k.Position != m.Position {
			return true
		}
	}
	return false
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
