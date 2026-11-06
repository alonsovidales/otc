// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const screenRunner = "../scripts/screen-runner/"

// The screen watcher's own checks (otc-screen-watch.py --selftest): which
// outputs count, the re-probing of a screen missed at boot, the install
// once, the browser started and stopped, the devices it never runs on.
func TestScreenWatcherSelftest(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	out, err := exec.Command(py, "-I", screenRunner+"otc-screen-watch.py", "--selftest").CombinedOutput()
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(out)), "selftest ok") {
		t.Fatalf("selftest: %v\n%s", err, out)
	}
}

// install.sh (new devices) and update.sh (every update from release 171 on,
// the first included) put the same four files in the same places, and the
// units run them from there.
func TestScreenRunnerInstalledTheSameEverywhere(t *testing.T) {
	line := regexp.MustCompile(`(?m)^\s*install(?:_atomic)? (?:-m )?(0[0-7]{3}) "\$SRC_DIR/scripts/screen-runner/([a-z.-]+)" (\S+)$`)
	installed := func(path string) []string {
		var got []string
		for _, m := range line.FindAllStringSubmatch(readFile(t, path), -1) {
			got = append(got, m[1]+" "+m[2]+" "+m[3])
		}
		sort.Strings(got)
		return got
	}
	want := []string{
		"0644 otc-screen-browser.service /etc/systemd/system/otc-screen-browser.service",
		"0644 otc-screen.service /etc/systemd/system/otc-screen.service",
		"0755 otc-screen-browser.sh /usr/local/bin/otc-screen-browser",
		"0755 otc-screen-watch.py /usr/local/bin/otc-screen-watch",
	}
	for _, script := range []string{"../scripts/install.sh", "../scripts/update.sh"} {
		if got := installed(script); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s installs\n%s\nwant\n%s", script, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
	for _, f := range []string{"otc-screen-watch.py", "otc-screen-browser.sh"} {
		if st, err := os.Stat(screenRunner + f); err != nil || st.Mode()&0o111 == 0 {
			t.Errorf("%s isn't executable in the repository", f)
		}
	}
	watch := readFile(t, screenRunner+"otc-screen.service")
	if !strings.Contains(watch, "\nExecStart=/usr/local/bin/otc-screen-watch\n") || !strings.Contains(watch, "\nUser=root\n") {
		t.Error("otc-screen.service doesn't run the watcher as root")
	}
	browser := readFile(t, screenRunner+"otc-screen-browser.service")
	for _, l := range []string{
		"ExecStartPre=+/usr/local/bin/otc-screen-browser --wait",
		"ExecStart=/usr/local/bin/otc-screen-browser",
		"User=otc-screen",
		"PAMName=login",
		"TTYPath=/dev/tty7",
		"EnvironmentFile=/run/otc-screen/browser.env",
	} {
		if !strings.Contains(browser, "\n"+l+"\n") {
			t.Errorf("otc-screen-browser.service has no %q", l)
		}
	}
	// Started by the watcher only: never enabled, so never at boot without
	// a screen.
	if strings.Contains(browser, "\n[Install]\n") {
		t.Error("otc-screen-browser.service has an [Install] section")
	}
	py := readFile(t, screenRunner+"otc-screen-watch.py")
	for _, s := range []string{`USER = "otc-screen"`, `BROWSER_UNIT = "otc-screen-browser.service"`,
		`self.env = root + "/run/otc-screen/browser.env"`} {
		if !strings.Contains(py, s) {
			t.Errorf("otc-screen-watch.py has no %s", s)
		}
	}
}

// The launcher takes only the address the watcher writes - the device's
// own web app on loopback, in screen mode - and refuses anything else
// before starting a thing.
func TestScreenBrowserTakesOnlyTheDevicesAddress(t *testing.T) {
	for _, u := range []string{"", "http://127.0.0.1:8080/", "https://127.0.0.1:8080/?screen=1",
		"http://example.com:8080/?screen=1", "http://127.0.0.1:8080/?screen=1&x=$(id)", "http://127.0.0.1:8080/?screen=1\n--x"} {
		cmd := exec.Command("bash", screenRunner+"otc-screen-browser.sh")
		cmd.Env = []string{"PATH=/nonexistent", "OTC_SCREEN_URL=" + u, "HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 || !strings.Contains(string(out), "OTC_SCREEN_URL") {
			t.Errorf("OTC_SCREEN_URL=%q: %v\n%s", u, err, out)
		}
	}
	py := readFile(t, screenRunner+"otc-screen-watch.py")
	if !strings.Contains(py, `return "http://127.0.0.1:%d/?screen=1" % port`) {
		t.Error("the watcher's address is no longer the one the launcher takes")
	}
}
