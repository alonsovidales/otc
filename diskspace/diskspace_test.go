// SPDX-License-Identifier: AGPL-3.0-or-later

package diskspace

import (
	"os"
	"testing"
)

// A 4 TB mirror with ext4's 5% for root: what the device can write is the
// available blocks, never the free ones, and the size it shows leaves the
// reserve out, so size minus used is what is left.
func TestUsageLeavesRootsReserveOut(t *testing.T) {
	const unit = 4096
	blocks := uint64(4_000_000_000_000 / unit)
	reserve := blocks / 20
	used := uint64(3_700_000_000_000 / unit)
	u := fromStatfs(blocks, blocks-used, blocks-used-reserve, unit)
	if u.Total != blocks*unit || u.Used != used*unit {
		t.Fatalf("total %d used %d", u.Total, u.Used)
	}
	if want := (blocks - used - reserve) * unit; u.Avail != want {
		t.Fatalf("avail %d, want %d (the free blocks less root's reserve)", u.Avail, want)
	}
	if u.Size()-u.Used != u.Avail {
		t.Fatalf("size %d - used %d is not what is left (%d)", u.Size(), u.Used, u.Avail)
	}
	if got := u.Percent(); got != 98 {
		t.Fatalf("percent %d, want 98 (3.7 TB of the 3.8 TB the device can use)", got)
	}
}

// df's Use%: rounded up, so a disk with a sliver left is full, and an
// empty or unknown one is 0.
func TestPercentRoundsUp(t *testing.T) {
	for _, c := range []struct {
		u    Usage
		want int
	}{
		{Usage{Used: 0, Avail: 100}, 0},
		{Usage{Used: 1, Avail: 999}, 1},
		{Usage{Used: 899, Avail: 101}, 90},
		{Usage{Used: 999, Avail: 1}, 100},
		{Usage{}, 0},
	} {
		if got := c.u.Percent(); got != c.want {
			t.Errorf("%+v: %d%%, want %d%%", c.u, got, c.want)
		}
	}
}

// Available blocks over the free ones (an estimate) never make the
// usable size larger than the disk.
func TestAvailNeverOverFree(t *testing.T) {
	u := fromStatfs(100, 10, 50, 1)
	if u.Avail != 10 || u.Size() != 100 {
		t.Fatalf("%+v", u)
	}
}

func TestOfAFolder(t *testing.T) {
	u, err := Of(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if u.Total == 0 || u.Used > u.Total || u.Size() > u.Total {
		t.Fatalf("%+v", u)
	}
	if _, err := Of(os.DevNull + "/not/a/folder"); err == nil {
		t.Fatal("no error for a path that doesn't exist")
	}
}
