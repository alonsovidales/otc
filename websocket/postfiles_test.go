// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// A post's files leave the device with a type that can't run script in its
// origin, rows stored before friends' types were checked included: the web
// app makes them blob: URLs there.
func TestPostFilesGoOutWithSafeTypes(t *testing.T) {
	files := []*pb.File{{Mime: "image/svg+xml"}, {Mime: "text/html"}, {Mime: "image/jpeg"}, {Mime: "video/mp4"}, {}}
	want := []string{"application/octet-stream", "application/octet-stream", "image/jpeg", "video/mp4", "application/octet-stream"}
	for i, f := range safePostFiles(files) {
		if f.Mime != want[i] {
			t.Errorf("file %d: %q, want %q", i, f.Mime, want[i])
		}
	}
}
