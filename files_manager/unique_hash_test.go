// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestUniqueByHashKeepsFirstInOrder(t *testing.T) {
	in := []*pb.File{{Path: "/a/1", Hash: "x"}, {Path: "/b/2", Hash: "y"}, {Path: "/c/1", Hash: "x"}, {Path: "/d/3", Hash: "z"}}
	out := uniqueByHash(in)
	want := []string{"/a/1", "/b/2", "/d/3"}
	if len(out) != len(want) {
		t.Fatalf("got %d files, want %d", len(out), len(want))
	}
	for i, p := range want {
		if out[i].Path != p {
			t.Errorf("file %d: got %s, want %s", i, out[i].Path, p)
		}
	}
}
