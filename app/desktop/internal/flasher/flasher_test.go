// SPDX-License-Identifier: AGPL-3.0-or-later

package flasher

import "testing"

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
