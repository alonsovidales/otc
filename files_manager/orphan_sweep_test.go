// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Only what nothing can reach any more goes: temp files, and archives and
// gallery copies with no row - never a blob, a thumbnail, another user's
// folder, a link that has its row, or something just written.
func TestSweepOrphansRemovesOnlyLeftovers(t *testing.T) {
	root := t.TempDir()
	storage, unenc := filepath.Join(root, "storage"), filepath.Join(root, "unenc")
	blob := strings.Repeat("ab", 32)
	kept := []string{
		"storage/" + blob,
		"storage/" + blob + "_thumbnail",
		"storage/.integrity-reported",
		"storage/11111111-2222-4333-8444-555555555555",               // an archive with its row
		"storage/shared/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee/0.orig", // a gallery with its row
		"storage/user_99999999-8888-4777-8666-555555555555/.blob-1",  // another instance's
		"storage/.blob-recent",                                       // being written
		"storage/ABCDEF01-2222-4333-8444-555555555555",               // not a canonical id
		"unenc/" + blob,
	}
	gone := []string{
		"storage/.blob-123",
		"storage/.upload-456",
		"storage/66666666-7777-4888-8999-000000000000",               // an archive with no row
		"storage/shared/12121212-3434-4565-8787-909090909090/0.orig", // a gallery with no row
		"unenc/.post-789.mp4",
		"unenc/.post-abc",
	}
	old := time.Now().Add(-time.Hour)
	for _, p := range append(append([]string{}, kept...), gone...) {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o750)
		if err := os.WriteFile(full, []byte("0123456789"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p, "recent") {
			os.Chtimes(full, old, old)
			os.Chtimes(filepath.Dir(full), old, old)
		}
	}
	links := map[string]bool{
		"11111111-2222-4333-8444-555555555555": true,
		"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee": true,
	}

	// The database couldn't be asked: no archive or gallery goes.
	n, _ := sweepOrphans(storage, unenc, nil, time.Now().Add(-time.Minute))
	if n != 4 {
		t.Errorf("with no link list, %d removed, want the 4 temp files", n)
	}
	if _, err := os.Stat(filepath.Join(root, "storage/66666666-7777-4888-8999-000000000000")); err != nil {
		t.Error("an archive was removed without knowing the links")
	}

	n, freed := sweepOrphans(storage, unenc, links, time.Now().Add(-time.Minute))
	if n != 2 || freed != 20 {
		t.Errorf("removed %d (%d bytes), want the archive and the gallery with no row (20 bytes)", n, freed)
	}
	for _, p := range kept {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
	for _, p := range gone {
		if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", p)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(storage, "shared"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Errorf("galleries left: %v", names)
	}
}

// The sweep runs after the listeners are up, while this process writes:
// what was modified after it started - a chunked upload paused for minutes,
// an archive being built - is never a leftover, and a folder of more
// entries than a batch is read through.
func TestSweepOrphansLeavesThisRunsWorkAndReadsInBatches(t *testing.T) {
	storage := t.TempDir()
	started := time.Now().Add(-10 * time.Minute)
	before := started.Add(-cOrphanMinAge)
	stale, paused := filepath.Join(storage, ".blob-stale"), filepath.Join(storage, ".blob-paused")
	for _, p := range []string{stale, paused} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The stale one is from before this run; the paused one was last
	// written five minutes ago, after this run started.
	old := started.Add(-time.Hour)
	os.Chtimes(stale, old, old)
	five := time.Now().Add(-5 * time.Minute)
	os.Chtimes(paused, five, five)
	for i := 0; i < cOrphanBatch+50; i++ {
		p := filepath.Join(storage, fmt.Sprintf("%064x", i))
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	last := filepath.Join(storage, ".upload-last")
	os.WriteFile(last, []byte("y"), 0o600)
	os.Chtimes(last, old, old)

	n, _ := sweepOrphans(storage, "", nil, before)
	if n != 2 {
		t.Errorf("removed %d, want the stale blob and upload", n)
	}
	if _, err := os.Stat(paused); err != nil {
		t.Error("this run's paused upload was removed")
	}
	for _, p := range []string{stale, last} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there", filepath.Base(p))
		}
	}
}
