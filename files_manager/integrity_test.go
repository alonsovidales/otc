// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// pagesOf is a hashPager over the files' and the versions' hashes.
func pagesOf(files, versions []string) hashPager {
	return func(v bool, after string, limit int) ([]string, error) {
		all := files
		if v {
			all = versions
		}
		var out []string
		for _, h := range all { // sorted, distinct
			if h > after && len(out) < limit {
				out = append(out, h)
			}
		}
		return out, nil
	}
}

// Issue #141: the daily check finds rows whose content is gone and
// reports them once, with their paths.
func TestMissingContentAndReport(t *testing.T) {
	paths := map[string][]string{
		"aa": {"/photos/a.jpg"},
		"bb": {"/photos/b.jpg", "/kim/b.jpg"},
		"cc": {"/photos/c.jpg"},
	}
	onDisk := map[string]bool{"aa": true}
	missing, err := missingHashes(pagesOf([]string{"aa", "bb"}, []string{"bb", "cc"}), func(h string) bool { return onDisk[h] })
	if err != nil || strings.Join(missing, ",") != "bb,cc" {
		t.Fatalf("missing = %v, %v", missing, err)
	}
	title, detail := integrityReport(missing, paths)
	if title != "3 files have lost their content" {
		t.Errorf("title = %q", title)
	}
	if !strings.HasPrefix(detail, "/kim/b.jpg; /photos/b.jpg; /photos/c.jpg.") {
		t.Errorf("detail = %q", detail)
	}
	if integrityDigest(missing) == integrityDigest([]string{"bb"}) {
		t.Error("a different set must give a different digest")
	}
}

func TestIntegrityReportCapsTheList(t *testing.T) {
	paths := map[string][]string{}
	var missing []string
	for i := 0; i < 20; i++ {
		h := string(rune('a'+i)) + "x"
		paths[h] = []string{"/f/" + h}
		missing = append(missing, h)
	}
	title, detail := integrityReport(missing, paths)
	if title != "20 files have lost their content" || !strings.Contains(detail, "; and 5 more.") {
		t.Errorf("title %q detail %q", title, detail)
	}
}

// Issue #141: a blob is replaced whole even when the old file can't be
// written to (an older installation's, owned by another account).
func TestWriteBlobReplacesAReadOnlyFile(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/abc"
	if err := os.WriteFile(target, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := writeBlob(target, []byte("content")); err != nil {
		t.Fatalf("writeBlob: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "content" {
		t.Fatalf("content = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

// The check reads the hashes a page at a time - every page, of the files
// and of the kept versions - never the whole library's paths, and asks
// whether each is on disk.
func TestMissingHashesPagesThroughEveryHash(t *testing.T) {
	var files []string
	for i := 0; i < 2*cIntegrityPage+7; i++ {
		files = append(files, fmt.Sprintf("%064x", i))
	}
	versions := []string{files[3], fmt.Sprintf("%064x", 1<<40)}
	pages := 0
	pager := pagesOf(files, versions)
	looked := map[string]int{}
	missing, err := missingHashes(func(v bool, after string, limit int) ([]string, error) {
		pages++
		return pager(v, after, limit)
	}, func(h string) bool {
		looked[h]++
		return h != files[cIntegrityPage+1] && h != versions[1]
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{files[cIntegrityPage+1], versions[1]}; strings.Join(missing, ",") != strings.Join(want, ",") {
		t.Fatalf("missing %v, want %v", missing, want)
	}
	if len(looked) != len(files)+1 {
		t.Fatalf("looked at %d hashes, want %d", len(looked), len(files)+1)
	}
	if pages != 4 { // three of the files, one of the versions
		t.Fatalf("%d pages", pages)
	}
}
