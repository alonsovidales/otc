// SPDX-License-Identifier: AGPL-3.0-or-later

package status

import (
	"strconv"
	"testing"

	"github.com/alonsovidales/otc/raidwatch"
)

// legacyDegraded is the status line exactly as it was built before the
// catalog (raidwatch's label for the missing disks, "" for none).
func legacyDegraded(active, total int, what string) string {
	msg := "RAID array degraded: only " + strconv.Itoa(active) + " of " + strconv.Itoa(total) + " disks active"
	if what != "" {
		msg += " - " + what + " stopped working"
	}
	return msg
}

// The degraded mirror's line is the same English as before for a request
// without a language, in English, or in a language this build has no text
// for: no disk known, each port, a disk with no known port, and two.
func TestDegradedTextEnglishUnchanged(t *testing.T) {
	top := raidwatch.Member{Position: "top"}
	bottom := raidwatch.Member{Position: "bottom"}
	unknown := raidwatch.Member{}
	cases := []struct {
		ports []raidwatch.Port
		label string
	}{
		{nil, ""},
		{[]raidwatch.Port{top.Where()}, top.Label()},
		{[]raidwatch.Port{bottom.Where()}, bottom.Label()},
		{[]raidwatch.Port{unknown.Where()}, unknown.Label()},
		{[]raidwatch.Port{top.Where(), bottom.Where()}, top.Label() + " and " + bottom.Label()},
	}
	for _, c := range cases {
		want := legacyDegraded(1, 2, c.label)
		for _, lang := range []string{"", "en", "en-GB", "xx"} {
			if got := degradedText(lang, 1, 2, c.ports, c.label); got != want {
				t.Errorf("lang %q, ports %v: got %q, want %q", lang, c.ports, got, want)
			}
		}
	}
	// The exact words, not only the old construction.
	if got := degradedText("", 1, 2, []raidwatch.Port{raidwatch.PortTop}, top.Label()); got != "RAID array degraded: only 1 of 2 disks active - the disk in the top blue USB port stopped working" {
		t.Errorf("top port: %q", got)
	}
	if got := degradedText("", 0, 2, nil, ""); got != "RAID array degraded: only 0 of 2 disks active" {
		t.Errorf("no port: %q", got)
	}
}
