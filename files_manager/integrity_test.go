// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"strings"
	"testing"
)

// Issue #141: the daily check finds rows whose content is gone and
// reports them once, with their paths.
func TestMissingContentAndReport(t *testing.T) {
	paths := map[string][]string{
		"aa": {"/photos/a.jpg"},
		"bb": {"/photos/b.jpg", "/kim/b.jpg"},
		"cc": {"/photos/c.jpg"},
	}
	onDisk := map[string]bool{"aa": true}
	missing := missingContent(paths, func(h string) bool { return onDisk[h] })
	if strings.Join(missing, ",") != "bb,cc" {
		t.Fatalf("missing = %v", missing)
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
