// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// raid-watch.service is started again 5 s after a failure, with no start
// limit: install.sh writes it so for new devices (Makefile.pi runs that
// installer too), and
// updates/169.sh adds the same two lines to the unit installed before -
// which, run once or twice, then reads exactly as install.sh writes it.
func TestRaidWatchUnitIsTheSameInInstallAndRelease169(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	heredoc := regexp.MustCompile(`(?s)cat > /etc/systemd/system/raid-watch\.service <<'EOF'\n(.*?\n)EOF\n`)
	m := heredoc.FindStringSubmatch(read("../scripts/install.sh"))
	if m == nil {
		t.Fatal("install.sh writes no raid-watch.service")
	}
	unit := m[1]
	for _, want := range []string{"[Unit]\nStartLimitIntervalSec=0\n", "[Service]\nRestartSec=5\n", "\nRestart=on-failure\n"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("install.sh's raid-watch.service lacks %q:\n%s", want, unit)
		}
	}

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash here")
	}
	fn := regexp.MustCompile(`(?ms)^dev_runtime_raid_watch_restart\(\) \{\n.*?^\}\n`).FindString(read("../scripts/updates/169.sh"))
	if fn == "" {
		t.Fatal("updates/169.sh has no dev_runtime_raid_watch_restart")
	}
	old := strings.Replace(strings.Replace(unit, "StartLimitIntervalSec=0\n", "", 1), "RestartSec=5\n", "", 1)
	path := filepath.Join(t.TempDir(), "raid-watch.service")
	for _, c := range []struct{ name, before string }{
		{"the unit install.sh wrote before", old},
		{"run again", unit},
		// One of the two set already (by hand): only the other is added.
		{"RestartSec already set", strings.Replace(old, "[Service]\n", "[Service]\nRestartSec=5\n", 1)},
	} {
		if err := os.WriteFile(path, []byte(c.before), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", "-c", fn+`dev_runtime_raid_watch_restart "$1"`, "bash", path).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", c.name, err, out)
		}
		if got := read(path); got != unit {
			t.Errorf("%s: the unit reads\n%s\nnot as install.sh writes it:\n%s", c.name, got, unit)
		}
	}
	if matches, _ := filepath.Glob(path + ".otc.*"); len(matches) != 0 {
		t.Errorf("temporary files left behind: %v", matches)
	}
}
