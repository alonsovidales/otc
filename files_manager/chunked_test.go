// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"os"
	"strings"
	"testing"
)

// A connection's unfinished uploads go when it closes - their temp files
// too - and nobody else's do.
func TestAbortUploadsOfDropsOnlyThatConnectionsUploads(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	mg := &Manager{}
	connA, connB := new(int), new(int)
	temps := func() int {
		n := 0
		entries, _ := os.ReadDir(storage)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".blob-") {
				n++
			}
		}
		return n
	}
	before := temps()
	idA, err := mg.BeginUpload(ses, "/a.mov", 10, false, nil, nil, "", connA)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := mg.BeginUpload(ses, "/b.mov", 10, false, nil, nil, "", connB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mg.UploadChunk(ses, idA, 0, []byte("12345")); err != nil {
		t.Fatal(err)
	}
	if got := temps(); got != before+2 {
		t.Fatalf("%d temp files, want %d", got, before+2)
	}

	mg.AbortUploadsOf(connA)
	if _, err := mg.UploadChunk(ses, idA, 5, []byte("67890")); err != ErrUnknownUpload {
		t.Errorf("the closed connection's upload is still there: %v", err)
	}
	if _, err := mg.UploadChunk(ses, idB, 0, []byte("12345")); err != nil {
		t.Errorf("another connection's upload was dropped: %v", err)
	}
	if got := temps(); got != before+1 {
		t.Errorf("%d temp files after the abort, want %d", got, before+1)
	}
	mg.AbortUploadsOf(connB)
	if got := temps(); got != before {
		t.Errorf("%d temp files left, want %d", got, before)
	}
}
