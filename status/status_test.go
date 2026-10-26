// SPDX-License-Identifier: AGPL-3.0-or-later

package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestDiskUsageRoot(t *testing.T) {
	all, used, free, err := diskUsage("/")
	if err != nil {
		t.Fatalf("diskUsage(\"/\") returned an error: %v", err)
	}

	if all == 0 {
		t.Error("expected a nonzero total disk size for \"/\"")
	}
	if used+free != all {
		t.Errorf("expected used (%d) + free (%d) to equal all (%d)", used, free, all)
	}
	if used > all {
		t.Errorf("used (%d) should never exceed all (%d)", used, all)
	}
}

func TestDiskUsageMissingPath(t *testing.T) {
	if _, _, _, err := diskUsage("/this/path/does/not/exist/hopefully"); err == nil {
		t.Error("expected an error for a nonexistent path")
	}
}

// Issue #65: the status widget should show RAID health (in sync, syncing,
// degraded) instead of the device's IP. Fixtures below are real /proc/mdstat
// content (the healthy one captured directly from Cala's own RAID1 array,
// the others adapted from real mdadm output for the states Cala's own
// array isn't currently in) - not hand-invented text, since the exact
// column spacing/wording is what the regexes actually have to survive.
const mdstatHealthy = `Personalities : [raid0] [raid1] [raid6] [raid5] [raid4] [raid10]
md0 : active raid1 sdb[2] sda[3]
      1001257984 blocks super 1.2 [2/2] [UU]
      bitmap: 0/2 pages [0KB], 65536KB chunk

unused devices: <none>
`

const mdstatSyncing = `Personalities : [raid1]
md0 : active raid1 sda[0] sdb[1]
      976630464 blocks super 1.2 [2/2] [UU]
      [=====>...............]  resync = 25.5% (250071232/976630464) finish=45.2min speed=90876K/sec
      bitmap: 2/8 pages [8KB], 65536KB chunk

unused devices: <none>
`

// [2/1]: this is deliberately the exact text a real degraded array
// produced (caught via a real device going degraded while testing this
// feature) - [N/M] is [raid_disks/working_disks], and reading it backwards
// (an actual bug this fixture now guards against) reported "only 2 of 1
// disks active" and "Disks: 1" instead of the correct "1 of 2"/"Disks: 2".
const mdstatDegraded = `Personalities : [raid1]
md0 : active raid1 sda[0]
      976630464 blocks super 1.2 [2/1] [U_]
      bitmap: 3/8 pages [12KB], 65536KB chunk

unused devices: <none>
`

const mdstatNone = `Personalities :
unused devices: <none>
`

func TestParseMdstatHealthy(t *testing.T) {
	info, found := parseMdstat(mdstatHealthy)
	if !found {
		t.Fatal("expected an array to be found")
	}
	if info.state != pb.RaidState_RaidInSync {
		t.Errorf("expected RaidInSync, got %v", info.state)
	}
	if info.level != "raid1" {
		t.Errorf("expected level %q, got %q", "raid1", info.level)
	}
	if info.devicesTotal != 2 || info.devicesActive != 2 {
		t.Errorf("expected 2/2 devices, got %d/%d", info.devicesActive, info.devicesTotal)
	}
	if info.syncPercent != 0 {
		t.Errorf("expected no sync percent when healthy, got %v", info.syncPercent)
	}
}

func TestParseMdstatSyncing(t *testing.T) {
	info, found := parseMdstat(mdstatSyncing)
	if !found {
		t.Fatal("expected an array to be found")
	}
	if info.state != pb.RaidState_RaidSyncing {
		t.Errorf("expected RaidSyncing, got %v", info.state)
	}
	if info.syncPercent != 25.5 {
		t.Errorf("expected sync percent 25.5, got %v", info.syncPercent)
	}
	// A resync in progress still reports full device counts (both disks
	// are present, just not yet fully mirrored) - only the sync line
	// distinguishes this from RaidInSync.
	if info.devicesTotal != 2 || info.devicesActive != 2 {
		t.Errorf("expected 2/2 devices during resync, got %d/%d", info.devicesActive, info.devicesTotal)
	}
}

func TestParseMdstatDegraded(t *testing.T) {
	info, found := parseMdstat(mdstatDegraded)
	if !found {
		t.Fatal("expected an array to be found")
	}
	if info.state != pb.RaidState_RaidDegraded {
		t.Errorf("expected RaidDegraded, got %v", info.state)
	}
	if info.devicesTotal != 2 || info.devicesActive != 1 {
		t.Errorf("expected 1/2 devices, got %d/%d", info.devicesActive, info.devicesTotal)
	}
}

func TestParseMdstatNoArray(t *testing.T) {
	_, found := parseMdstat(mdstatNone)
	if found {
		t.Error("expected no array to be found in mdstat content with none listed")
	}
}

func TestReadRaidStatusMissingFile(t *testing.T) {
	orig := mdstatPath
	mdstatPath = filepath.Join(t.TempDir(), "does-not-exist")
	defer func() { mdstatPath = orig }()

	info := readRaidStatus()
	if info.state != pb.RaidState_RaidNone {
		t.Errorf("expected RaidNone when /proc/mdstat doesn't exist, got %v", info.state)
	}
}

func TestReadRaidStatusReadsRealFixture(t *testing.T) {
	orig := mdstatPath
	path := filepath.Join(t.TempDir(), "mdstat")
	if err := os.WriteFile(path, []byte(mdstatHealthy), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	mdstatPath = path
	defer func() { mdstatPath = orig }()

	info := readRaidStatus()
	if info.state != pb.RaidState_RaidInSync {
		t.Errorf("expected RaidInSync reading the fixture file, got %v", info.state)
	}
}

// In a container /proc/mdstat is the host's: a degraded host array (a
// NAS's system array, say) is no business of the device's, which reports
// no mirror and raises no banner. Without OTC_CONTAINER the same file is
// read as always.
func TestReadRaidStatusInAContainer(t *testing.T) {
	orig := mdstatPath
	path := filepath.Join(t.TempDir(), "mdstat")
	if err := os.WriteFile(path, []byte(mdstatDegraded), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	mdstatPath = path
	defer func() { mdstatPath = orig }()

	t.Setenv("OTC_CONTAINER", "1")
	if info := readRaidStatus(); info.state != pb.RaidState_RaidNone || info.devicesTotal != 0 || info.level != "" {
		t.Errorf("in a container: %+v, want no mirror", info)
	}

	t.Setenv("OTC_CONTAINER", "")
	if info := readRaidStatus(); info.state != pb.RaidState_RaidDegraded || info.devicesTotal != 2 {
		t.Errorf("elsewhere: %+v, want the degraded array", info)
	}
}

// The memory figures in a container: the container's when it has them,
// the machine's otherwise (never zero for a figure the machine has).
func TestContainerMemory(t *testing.T) {
	t.Setenv("OTC_CONTAINER", "1")
	t.Setenv("OTC_MEMORY_GB", "2")
	total, used := containerMemory(64<<30, 5<<30)
	if total != 2<<30 {
		t.Errorf("total %d, want the 2 GB the device is sized to", total)
	}
	if used == 0 {
		t.Error("used is 0")
	}
}

// A NAS's /proc/mdstat as a container sees it: the host's own arrays
// around the container's mirror, md127, here rebuilding onto a new disk.
const mdstatHostAndMirror = `Personalities : [raid1] [linear] [raid0] [raid10] [raid6] [raid5] [raid4]
md2 : active raid1 sata1p3[0]
      1942745600 blocks super 1.2 [1/1] [U]

md127 : active raid1 sde[2] sdc[0]
      3906886464 blocks super 1.2 [2/1] [U_]
      [>....................]  recovery =  3.2% (125024256/3906886464) finish=312.9min speed=201408K/sec
      bitmap: 0/30 pages [0KB], 65536KB chunk

md1 : active raid1 sata1p2[0]
      2097088 blocks [16/1] [U_______________]

md0 : active raid1 sata1p1[0]
      2490176 blocks [16/1] [U_______________]

unused devices: <none>
`

// In a container with its own mirror the status is that array's, found
// by its name among the host's - never the first array listed, nor one
// whose name starts the same.
func TestReadRaidStatusTheContainersMirror(t *testing.T) {
	orig := mdstatPath
	defer func() { mdstatPath = orig }()
	write := func(content string) {
		mdstatPath = filepath.Join(t.TempDir(), "mdstat")
		if err := os.WriteFile(mdstatPath, []byte(content), 0644); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
	}
	t.Setenv("OTC_CONTAINER", "1")
	t.Setenv("OTC_MIRROR_ARRAY", "md127")

	write(mdstatHostAndMirror)
	if info := readRaidStatus(); info.state != pb.RaidState_RaidSyncing || info.syncPercent != 3.2 || info.devicesTotal != 2 || info.devicesActive != 1 || info.level != "raid1" {
		t.Errorf("rebuilding: %+v", info)
	}

	healthy := strings.Replace(mdstatHostAndMirror, "[2/1] [U_]\n      [>....................]  recovery =  3.2% (125024256/3906886464) finish=312.9min speed=201408K/sec", "[2/2] [UU]", 1)
	write(healthy)
	if info := readRaidStatus(); info.state != pb.RaidState_RaidInSync || info.devicesTotal != 2 || info.devicesActive != 2 {
		t.Errorf("whole, the host's arrays degraded: %+v", info)
	}

	t.Setenv("OTC_MIRROR_ARRAY", "md_otc") // as the host's mdadm may name it
	write(strings.Replace(healthy, "md127 :", "md_otc :", 1))
	if info := readRaidStatus(); info.state != pb.RaidState_RaidInSync || info.devicesActive != 2 {
		t.Errorf("md_otc: %+v", info)
	}
	t.Setenv("OTC_MIRROR_ARRAY", "md127")

	degraded := strings.Replace(healthy, "md127 : active raid1 sde[2] sdc[0]\n      3906886464 blocks super 1.2 [2/2] [UU]", "md127 : active raid1 sdc[0]\n      3906886464 blocks super 1.2 [2/1] [U_]", 1)
	write(degraded)
	if info := readRaidStatus(); info.state != pb.RaidState_RaidDegraded || info.devicesActive != 1 {
		t.Errorf("degraded: %+v", info)
	}

	// Gone from the file, or another name: no mirror, whatever else is
	// there.
	t.Setenv("OTC_MIRROR_ARRAY", "md12")
	if info := readRaidStatus(); info.state != pb.RaidState_RaidNone || info.devicesTotal != 0 {
		t.Errorf("md12, not listed: %+v", info)
	}
	t.Setenv("OTC_MIRROR_ARRAY", "md1")
	if info := readRaidStatus(); info.devicesTotal != 16 {
		t.Errorf("md1 is the host's md1, not md127: %+v", info)
	}
	t.Setenv("OTC_MIRROR_ARRAY", "")
	if info := readRaidStatus(); info.state != pb.RaidState_RaidNone {
		t.Errorf("no mirror: %+v", info)
	}
}

func TestMdstatArray(t *testing.T) {
	want := "md127 : active raid1 sde[2] sdc[0]\n      3906886464 blocks super 1.2 [2/1] [U_]\n      [>....................]  recovery =  3.2% (125024256/3906886464) finish=312.9min speed=201408K/sec\n      bitmap: 0/30 pages [0KB], 65536KB chunk"
	if got := mdstatArray(mdstatHostAndMirror, "md127"); got != want {
		t.Errorf("md127:\n got %q\nwant %q", got, want)
	}
	if got := mdstatArray(mdstatHostAndMirror, "md0"); got != "md0 : active raid1 sata1p1[0]\n      2490176 blocks [16/1] [U_______________]" {
		t.Errorf("md0: %q", got)
	}
	// The last array, with no blank line before the end.
	if got := mdstatArray("md127 : active raid1 sdc[0] sdd[1]\n      10 blocks [2/2] [UU]", "md127"); got != "md127 : active raid1 sdc[0] sdd[1]\n      10 blocks [2/2] [UU]" {
		t.Errorf("at the end: %q", got)
	}
	for _, name := range []string{"md12", "md1270", "Personalities", "unused devices", "blocks super 1.2 [2/1] [U_]"} {
		if got := mdstatArray(mdstatHostAndMirror, name); got != "" {
			t.Errorf("%q: %q", name, got)
		}
	}
}
