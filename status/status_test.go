// SPDX-License-Identifier: AGPL-3.0-or-later

package status

import (
	"os"
	"path/filepath"
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
