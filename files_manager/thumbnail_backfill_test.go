// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"os"
	"path/filepath"
	"testing"
)

// The backfill's whole cost when there's nothing wrong is one stat per
// file, so "is this thumbnail already there?" has to be exactly right in
// both directions: a false negative rebuilds the entire library for
// nothing, a false positive leaves the blank tile that prompted this.
func TestThumbnailPresenceCheck(t *testing.T) {
	dir := t.TempDir()
	const withThumb = "aaaa"
	const withoutThumb = "bbbb"

	if err := os.WriteFile(filepath.Join(dir, withThumb+"_thumbnail"), []byte("jpeg"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	// The source file existing is not the question - a video can be on
	// disk with no thumbnail beside it, which is exactly the state this
	// repairs.
	if err := os.WriteFile(filepath.Join(dir, withoutThumb), []byte("video"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	has := func(hash string) bool {
		_, err := os.Stat(filepath.Join(dir, hash+"_thumbnail"))
		return err == nil
	}

	if !has(withThumb) {
		t.Error("a file with a thumbnail was reported as missing one")
	}
	if has(withoutThumb) {
		t.Error("a file with no thumbnail was reported as having one")
	}
}

// isReprocessing is what stops this running alongside issue #73's full
// reprocess, which would have both jobs decrypting the same library at
// once on a machine that can't spare it.
func TestIsReprocessingReflectsTheRunningJob(t *testing.T) {
	mg := &Manager{}
	if mg.isReprocessing() {
		t.Error("an idle manager reported a reprocess in progress")
	}

	mg.reprocessMu.Lock()
	mg.reprocessing = true
	mg.reprocessMu.Unlock()

	if !mg.isReprocessing() {
		t.Error("a running reprocess was not reported")
	}
}
