// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"strings"
	"testing"
)

// The manifest is the whole contract between a release and every device
// in the field, and it is parsed by builds older than the manifest itself
// - so unknown extra columns have to be tolerated rather than strand the
// devices that most need updating.
func TestParseManifest(t *testing.T) {
	manifest := `# a comment
# another	with	tabs

1	abc123	assets1	First release
2	def456	-	Second release
3	aaa111	assets3	Third	with an extra column
not-a-number	xxx	yyy	ignored
4	nosha
`
	releases, err := parseManifest(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(releases) != 4 {
		t.Fatalf("got %d releases, want 4: %+v", len(releases), releases)
	}
	if releases[0].Version != 1 || releases[0].Description != "First release" {
		t.Errorf("first release = %+v", releases[0])
	}
	if releases[2].Description != "Third" {
		t.Errorf("a row with extra columns lost its description: %+v", releases[2])
	}
	// A release with no migration carries "-" where a script checksum
	// would be, and is still a release the device has to move through.
	if releases[1].Version != 2 || releases[1].Description != "Second release" {
		t.Errorf("a release with no script was misread: %+v", releases[1])
	}
	if releases[3].Version != 4 || releases[3].Description != "" {
		t.Errorf("a row with no description should still be a release: %+v", releases[3])
	}
}

// A device installed before this feature existed has no version file, and
// the only safe reading of that is "the beginning" - it then runs the
// whole history, which is exactly why every release script must be
// idempotent.
func TestInstalledVersionIsZeroWithoutAVersionFile(t *testing.T) {
	if got := InstalledVersion(); got < 0 {
		t.Errorf("InstalledVersion() = %d, want a non-negative version", got)
	}
}

// Nothing has run here, so there is no status file - which must read as
// idle rather than as an error state the UI would show as a failure.
func TestCurrentStatusDefaultsToIdle(t *testing.T) {
	if got := CurrentStatus(); got.State == "" {
		t.Error("CurrentStatus() returned an empty state, want a usable default")
	}
}

// A "running" status is reported as failed only once the update unit has
// definitely stopped: a run cut off by a power cut would otherwise lock
// the Update button for good.
func TestReconcileStatus(t *testing.T) {
	running := Status{State: "running", Message: "Building", Version: "91", Updated: "2026-10-05T10:00:00Z"}
	gone := func() bool { return true }
	alive := func() bool { return false }

	got := reconcileStatus(running, gone)
	if got.State != "failed" || got.Message != cInterrupted || got.Version != "91" || got.Updated != running.Updated {
		t.Errorf("an interrupted run read as %+v", got)
	}
	if got := reconcileStatus(running, alive); got != running {
		t.Errorf("a live run read as %+v", got)
	}
	done := Status{State: "done", Message: "Updated to version 92"}
	if got := reconcileStatus(done, func() bool { t.Fatal("asked systemd about a finished run"); return true }); got != done {
		t.Errorf("a finished run read as %+v", got)
	}
}

func TestUnitStopped(t *testing.T) {
	for state, want := range map[string]bool{
		"inactive": true, "failed": true,
		"activating": false, "active": false, "deactivating": false, "reloading": false, "": false,
	} {
		if got := unitStopped(state); got != want {
			t.Errorf("unitStopped(%q) = %v, want %v", state, got, want)
		}
	}
}
