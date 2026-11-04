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

// install.sh builds OpenCV from source where the system's is too old, in a
// directory of its own under /var/tmp with the build's log inside. The
// block, run with stand-ins for the build tools: the directory goes once
// OpenCV is found, and when pkg-config can't find what the build installed,
// the log the error points at is still there.
func TestOpenCVBuildKeepsTheLogItPointsAt(t *testing.T) {
	install, err := os.ReadFile("../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?ms)^if ! opencv_ok; then\n    wiz_step opencv .*?\n    log "OpenCV \$\(pkg-config --modversion opencv4\) installed in /usr/local"\nfi\n`).FindString(string(install))
	if block == "" {
		t.Fatal("scripts/install.sh has no OpenCV build block")
	}

	run := func(foundAfter bool) (tmp, out string) {
		t.Helper()
		work := t.TempDir()
		tmp = filepath.Join(work, "var-tmp")
		bin := filepath.Join(work, "bin")
		for _, d := range []string{tmp, bin} {
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		meminfo := filepath.Join(work, "meminfo")
		if err := os.WriteFile(meminfo, []byte("MemAvailable:    4000000 kB\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		tools := map[string]string{
			"apt-get":    "exit 0",
			"ldconfig":   "exit 0",
			"nproc":      "echo 2",
			"pkg-config": "exit 1",
			"df":         `printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/x 99999999 1 99999998 1%% /\n'`,
			// tar -xzf <archive> -C <dir>: the source tree, unpacked.
			"tar": `while [ "$#" -gt 0 ]; do [ "$1" = -C ] && mkdir -p "$2/opencv-4.10.0" && echo unpacked; shift; done`,
			// cmake --build shows progress; configure and install say so.
			"cmake": `case " $* " in *" --build "*) mkdir -p "$3"; echo "[ 50%] Building"; echo "[100%] Built target opencv_core" ;; *) echo "cmake $1" ;; esac`,
		}
		for name, body := range tools {
			if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		calls := filepath.Join(work, "opencv_ok-calls")
		script := `set -euo pipefail
log() { echo "[otc-install] $*"; }
die() { echo "[otc-install] ERROR: $*" >&2; exit 1; }
wiz_step() { :; }
wiz_error() { echo "@error $*" >&2; }
fetch_pinned() { echo archive > "$2"; }
OPENCV_MIN=4.10
OPENCV_SRC_VERSION=4.10.0
OPENCV_SRC_SHA=0
# Too old at first (or the block wouldn't run), then as FOUND_AFTER says.
opencv_ok() {
    echo x >> "$CALLS"
    [ "$(wc -l < "$CALLS")" -gt 1 ] && [ "$FOUND_AFTER" = 1 ]
}
` + strings.NewReplacer("/var/tmp", tmp, "/proc/meminfo", meminfo).Replace(block)
		found := "0"
		if foundAfter {
			found = "1"
		}
		cmd := exec.Command("/bin/bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CALLS="+calls, "FOUND_AFTER="+found)
		b, _ := cmd.CombinedOutput()
		return tmp, string(b)
	}

	// Built, but not found: the error names the log, and the log is there,
	// with the build's output; the sources and the build tree are gone.
	tmp, out := run(false)
	m := regexp.MustCompile(`ERROR: could not build OpenCV 4\.10\.0 - see (\S+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("not found after the build: no error naming the log\n%s", out)
	}
	if log, err := os.ReadFile(m[1]); err != nil || !strings.Contains(string(log), "Built target opencv_core") {
		t.Errorf("not found after the build: the log the error points at (%s) is gone or empty: %v\n%s", m[1], err, out)
	}
	if !strings.Contains(out, "@error opencv_build version 4.10.0 path "+m[1]) {
		t.Errorf("not found after the build: the wizard isn't pointed at the log\n%s", out)
	}
	dir := filepath.Dir(m[1])
	for _, gone := range []string{"src.tar.gz", "opencv-4.10.0", "build"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("not found after the build: %s was left behind", gone)
		}
	}

	// Built and found: nothing is left in /var/tmp.
	tmp, out = run(true)
	if !strings.Contains(out, "installed in /usr/local") {
		t.Fatalf("found after the build: no success\n%s", out)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("found after the build: %d entries left in /var/tmp\n%s", len(left), out)
	}
}
