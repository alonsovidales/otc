// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The root runners that a request file in the otc user's /var/lib/otc
// starts (a path unit with PathExists=, which starts the runner again for
// as long as the path exists) remove whatever is there with rm -rf --: a
// directory there, which rm -f can't remove, had its path unit start the
// runner back to back for good. -r never follows a symlink.
func TestRunnersRemoveWhateverIsAtTheirRequest(t *testing.T) {
	removed := regexp.MustCompile(`(?m)^(?:\[ "\$dry" = 1 \] \|\| )?rm -rf -- "\$REQUEST"$`)
	request := regexp.MustCompile(`(?m)^REQUEST=(/var/lib/otc/[a-z-]+\.request)$`)
	for _, r := range []struct{ script, path string }{
		{"../scripts/update-runner/otc-update-runner.sh", "../scripts/update-runner/otc-update.path"},
		{"../scripts/bridge-runner/otc-bridge-runner.sh", "../scripts/bridge-runner/otc-bridge.path"},
		{"../scripts/tailscale-runner/otc-tailscale-runner.sh", "../scripts/tailscale-runner/otc-tailscale.path"},
		{"../scripts/device-runner/otc-reboot-runner.sh", "../scripts/device-runner/otc-reboot.path"},
		{"../scripts/device-runner/otc-poweroff-runner.sh", "../scripts/device-runner/otc-poweroff.path"},
		{"../scripts/device-runner/otc-wifi-restart-runner.sh", "../scripts/device-runner/otc-wifi-restart.path"},
	} {
		b, err := os.ReadFile(r.script)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !removed.MatchString(s) || strings.Contains(s, `rm -f "$REQUEST"`) {
			t.Errorf("%s doesn't remove its request with rm -rf --", r.script)
		}
		m := request.FindStringSubmatch(s)
		if m == nil {
			t.Errorf("%s names no request in /var/lib/otc", r.script)
			continue
		}
		unit, err := os.ReadFile(r.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(unit), "\nPathExists="+m[1]+"\n") {
			t.Errorf("%s doesn't watch %s, the request %s removes", r.path, m[1], r.script)
		}
	}
}
