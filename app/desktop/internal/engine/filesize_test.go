// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #187: a device from release 93 sends size64 with the wrapped int32
// size beside it; an older one only the size, read as it always was.
func TestFileSize(t *testing.T) {
	big := int64(3) << 30 // 3 GiB: wraps to a negative int32
	cases := []struct {
		name string
		f    *pb.File
		want int64
	}{
		{"new device, large file", &pb.File{Size64: big, Size: int32(big)}, big},
		{"new device, small file", &pb.File{Size64: 1234, Size: 1234}, 1234},
		{"new device, empty file", &pb.File{}, 0},
		{"old device, small file", &pb.File{Size: 1234}, 1234},
		{"old device, large file", &pb.File{Size: int32(big)}, int64(int32(big))},
	}
	for _, c := range cases {
		if got := fileSize(c.f); got != c.want {
			t.Errorf("%s: fileSize = %d, want %d", c.name, got, c.want)
		}
	}
}
