// SPDX-License-Identifier: AGPL-3.0-or-later

package flasher

import (
	"strings"
	"testing"
)

func TestShortID(t *testing.T) {
	for id, want := range map[string]string{`\\.\PhysicalDrive2`: "disk 2", "/dev/sdb": "sdb", "/dev/mmcblk0": "mmcblk0"} {
		if got := (Disk{ID: id}).ShortID(); got != want {
			t.Errorf("ShortID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestPadded(t *testing.T) {
	buf := make([]byte, 2048)
	for i := range buf {
		buf[i] = 0xff
	}
	p := padded(buf, 700)
	if len(p) != 1024 || p[699] != 0xff || p[700] != 0 || p[1023] != 0 {
		t.Fatalf("padded: len %d", len(p))
	}
	if b := alignedBuf(cChunk); uintptrOf(b)%4096 != 0 || len(b) != cChunk {
		t.Fatal("alignedBuf is not page-aligned")
	}
}

// The writer only writes to the disk that was confirmed: same ID, and -
// when the confirming side sent them - the same size and name.
func TestMatchDisk(t *testing.T) {
	card := Disk{ID: "/dev/sdb", Name: "SanDisk Ultra", Size: 32_000_000_000}
	now := []Disk{{ID: "/dev/sda", Name: "Backup", Size: 2_000_000_000_000}, card}
	if d, err := matchDisk(now, card); err != nil || d.ID != card.ID {
		t.Fatalf("the confirmed card: %v %v", d, err)
	}
	if d, err := matchDisk(now, Disk{ID: "/dev/sdb"}); err != nil || d.Size != card.Size {
		t.Fatalf("an older tray (ID only): %v %v", d, err)
	}
	swapped := []Disk{{ID: "/dev/sdb", Name: "WD Elements", Size: 2_000_000_000_000}}
	if _, err := matchDisk(swapped, card); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("another disk under the same name was accepted: %v", err)
	}
	if _, err := matchDisk(now[:1], card); err == nil {
		t.Fatal("a disk that is gone was accepted")
	}
}
