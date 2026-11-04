// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package flasher

import "testing"

func TestUnderAMountPoint(t *testing.T) {
	for _, tc := range []struct {
		p, top string
		want   bool
	}{
		{"/media/ana/Backup/Photos", "/media/ana/Backup", true},
		{"/media/ana/Backup", "/media/ana/Backup", true},
		{"/media/ana/Backup2/Photos", "/media/ana/Backup", false},
		{"/home/ana/Sync", "/", true},
		{"/mnt/nas/x", "/mnt/nas/", true},
	} {
		if got := under(tc.p, tc.top); got != tc.want {
			t.Errorf("under(%q, %q) = %v", tc.p, tc.top, got)
		}
	}
}

// Only what the desktop mounted for a card is unmounted by the writer.
func TestAutomounted(t *testing.T) {
	for target, want := range map[string]bool{
		"/media/ana/BOOT": true, "/run/media/ana/rootfs": true, "/mnt/backup": false, "/srv/data": false, "/media": false,
	} {
		if got := automounted(target); got != want {
			t.Errorf("automounted(%q) = %v", target, got)
		}
	}
}
