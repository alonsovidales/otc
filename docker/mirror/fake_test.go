// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const ourUUID = "11111111:22222222:33333333:44444444"

var clock = time.Date(2026, 10, 26, 10, 0, 0, 0, time.FixedZone("CET", 3600))

// fake is a server with two disks, sdb (8:16) and sdc (8:32), named
// ata-ONE and ata-TWO in .env: a sysfs tree on disk, the device nodes,
// what holds them, and mdadm and friends answering from a script.
type fake struct {
	t          *testing.T
	root       string
	h          *host
	nodes      map[string]string   // device node -> major:minor
	busy       map[string]error    // device node -> why an exclusive open fails
	unreadable map[string]error    // device node -> why reading its ends fails
	fs         map[string]string   // device -> filesystem blkid reports
	answers    map[string][]result // command line -> answers, the last one kept
	mdName     string              // MD_NAME of the array, for mdadm --detail
	calls      []string
	mknods     []string
	synced     bool
	out        bytes.Buffer
	errb       bytes.Buffer
}

func newFake(t *testing.T) *fake {
	f := &fake{
		t: t, root: t.TempDir(), nodes: map[string]string{}, busy: map[string]error{}, fs: map[string]string{}, unreadable: map[string]error{},
		answers: map[string][]result{}, mdName: "any:otc",
	}
	f.h = &host{
		sys: filepath.Join(f.root, "sys"), dev: "/dev", proc: filepath.Join(f.root, "proc"), mount: "/mnt/storage",
		run: f.run, devOf: f.devOf, free: f.free, mknod: f.mknod, sync: func() { f.synced = true },
		readEnds: func(path string) error { return f.unreadable[path] },
		stdout:   &f.out, stderr: &f.errb,
	}
	f.h.log = &storageLog{path: filepath.Join(f.root, "log/storage.log"), max: storageLogMax, now: func() time.Time { return clock }, stderr: &f.errb}
	f.h.disks = [2]disk{
		{n: 1, path: "/dev/otc-disk1", name: "/dev/disk/by-id/ata-ONE", label: "ata-ONE"},
		{n: 2, path: "/dev/otc-disk2", name: "/dev/disk/by-id/ata-TWO", label: "ata-TWO"},
	}
	f.disk(1, "sdb", "8:16", 4e12)
	f.disk(2, "sdc", "8:32", 4e12)
	f.write("block/sda/dev", "8:0") // the host's own disk
	return f
}

func (f *fake) write(rel, s string) {
	p := filepath.Join(f.h.sys, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// disk plugs a disk of that many bytes in as /dev/otc-disk<n>.
func (f *fake) disk(n int, name, id string, size float64) {
	f.nodes[fmt.Sprintf("/dev/otc-disk%d", n)] = id
	f.write("class/block/"+name+"/size", fmt.Sprint(int64(size/512)))
	link := filepath.Join(f.h.sys, "dev/block", id)
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	_ = os.Remove(link)
	if err := os.Symlink("../../class/block/"+name, link); err != nil {
		f.t.Fatal(err)
	}
}

type fm struct{ name, id, state string }

var (
	sdb = fm{"sdb", "8:16", "in_sync"}
	sdc = fm{"sdc", "8:32", "in_sync"}
)

func (m fm) is(state string) fm { m.state = state; return m }

// array puts an array in sysfs, running, using all of the 4 TB disks.
func (f *fake) array(md, level string, raidDisks int, members ...fm) {
	_ = os.RemoveAll(filepath.Join(f.h.sys, "block", md))
	f.write("block/"+md+"/dev", "9:"+strings.TrimPrefix(md, "md"))
	f.write("block/"+md+"/md/array_state", "clean")
	f.write("block/"+md+"/md/level", level)
	f.write("block/"+md+"/md/raid_disks", fmt.Sprint(raidDisks))
	f.write("block/"+md+"/md/sync_action", "idle")
	f.write("block/"+md+"/md/sync_completed", "none")
	f.write("block/"+md+"/md/component_size", "3906118928") // (4e12/512 - 262144) * 512 / 1024
	working := 0
	for i, m := range members {
		f.member(md, m)
		slot := "none"
		if m.state == "in_sync" {
			working++
			slot = fmt.Sprint(i)
		}
		f.write("block/"+md+"/md/dev-"+m.name+"/slot", slot)
	}
	f.write("block/"+md+"/md/degraded", fmt.Sprint(max(raidDisks-working, 0)))
}

func (f *fake) member(md string, m fm) {
	p := "block/" + md + "/md/dev-" + m.name + "/"
	f.write(p+"block/dev", m.id)
	f.write(p+"state", m.state)
	f.write(p+"offset", "262144")
}

func (f *fake) set(md, file, value string) { f.write("block/"+md+"/md/"+file, value) }

// on scripts a command's answers: each call takes the next, the last stays.
func (f *fake) on(line string, rs ...result) { f.answers[line] = rs }

// superblock: the disk holds part of an array.
func (f *fake) superblock(path, uuid, level, devices string) {
	f.on("mdadm --examine --export "+path, result{out: fmt.Sprintf("MD_LEVEL=%s\nMD_DEVICES=%s\nMD_NAME=any:otc\nMD_UUID=%s\nMD_DEV_UUID=aa:bb\n", level, devices, uuid)})
}

var kernel = map[string]fm{"/dev/otc-disk1": sdb, "/dev/otc-disk2": sdc}

func (f *fake) run(name string, args ...string) result {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if rs := f.answers[line]; len(rs) > 0 {
		if len(rs) > 1 {
			f.answers[line] = rs[1:]
		}
		return rs[0]
	}
	last := args[len(args)-1]
	switch {
	case strings.HasPrefix(line, "mdadm --examine --export "):
		return result{code: 1, err: "mdadm: No md superblock detected on " + last + "."}
	case strings.HasPrefix(line, "blkid -p -o export "):
		if t := f.fs[last]; t != "" {
			return result{out: "DEVNAME=" + last + "\nTYPE=" + t + "\n"}
		}
		return result{code: 2}
	case strings.HasPrefix(line, "mdadm --detail --export "):
		return result{out: "MD_LEVEL=raid1\nMD_DEVICES=2\nMD_NAME=" + f.mdName + "\nMD_UUID=" + ourUUID + "\n"}
	case strings.HasPrefix(line, "mdadm --assemble "), strings.HasPrefix(line, "mdadm --create "):
		var ms []fm
		for _, a := range args {
			if m, ok := kernel[a]; ok {
				ms = append(ms, m)
			}
		}
		f.array("md127", "raid1", 2, ms...)
		if args[0] == "--create" {
			f.set("md127", "sync_action", "resync")
			f.set("md127", "sync_completed", "0 / 3906250000")
		}
		f.nodes["/dev/md127"] = "9:127" // mdadm makes the node
	case strings.HasPrefix(line, "mdadm --add /dev/md127 "):
		f.member("md127", kernel[last].is("spare"))
		f.set("md127", "sync_action", "recover")
	case name == "mkfs.ext4":
		f.fs[last] = "ext4"
	}
	return result{}
}

func (f *fake) devOf(path string) (string, error) {
	if id, ok := f.nodes[path]; ok {
		return id, nil
	}
	return "", fmt.Errorf("stat %s: no such file or directory", path)
}

func (f *fake) free(path string) error { return f.busy[path] }

func (f *fake) mknod(path, id string) error {
	f.mknods = append(f.mknods, path+" "+id)
	f.nodes[path] = id
	return nil
}

// changes is every command run that changes something.
func (f *fake) changes() []string {
	var out []string
	for _, c := range f.calls {
		if !slices.ContainsFunc([]string{"mdadm --examine", "mdadm --detail", "blkid ", "wipefs --no-act", "blockdev ", "dumpe2fs "},
			func(p string) bool { return strings.HasPrefix(c, p) }) {
			out = append(out, c)
		}
	}
	return out
}

// said is what was written to stderr since the last call.
func (f *fake) said() []string {
	s := strings.TrimSpace(f.errb.String())
	f.errb.Reset()
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func (f *fake) wantChanges(want ...string) {
	f.t.Helper()
	if got := f.changes(); !slices.Equal(got, want) {
		f.t.Fatalf("changes:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func wantErr(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one with %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("error %q\ndoesn't say %q", err, p)
		}
	}
}

func wantSaid(t *testing.T, lines []string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, p) }) {
			t.Fatalf("nothing said %q in:\n  %s", p, strings.Join(lines, "\n  "))
		}
	}
}
