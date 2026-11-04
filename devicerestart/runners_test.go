// SPDX-License-Identifier: AGPL-3.0-or-later

package devicerestart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The root runners behind Restart device and Shut down device
// (scripts/device-runner/otc-reboot-runner.sh, otc-poweroff-runner.sh),
// each run on a copy whose paths are in a temporary directory, with
// systemctl stood in for: the request removed first (a directory too),
// dropped in the first two minutes after boot, while an update was asked
// for or runs (its request, its unit, update.sh's lock) and while a Wi-Fi
// change holds its lock; otherwise the machine restarted or powered off.
// --dry-run says the same and touches nothing. Needs bash and flock
// (util-linux): Linux.
func TestPowerRunnersRefuseDuringAnUpdateOrAWifiChange(t *testing.T) {
	for _, tool := range []string{"bash", "flock"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s here", tool)
		}
	}
	for _, r := range []struct{ script, request, action string }{
		{"otc-reboot-runner.sh", "reboot.request", "reboot"},
		{"otc-poweroff-runner.sh", "poweroff.request", "poweroff"},
	} {
		t.Run(r.action, func(t *testing.T) { runnerCases(t, r.script, r.request, r.action) })
	}
}

type runnerWorld struct {
	t                         *testing.T
	dir, script, request, log string
	updateRequest, updateLock string
	wifiLock                  string
}

func runnerCases(t *testing.T, script, request, action string) {
	src, err := os.ReadFile(filepath.Join("..", "scripts", "device-runner", script))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	w := &runnerWorld{t: t, dir: dir, request: filepath.Join(dir, request), log: filepath.Join(dir, "systemctl.log"),
		updateRequest: filepath.Join(dir, "update.request"), updateLock: filepath.Join(dir, "otc-update.lock"),
		wifiLock: filepath.Join(dir, "otc-wifi.lock")}
	body := string(src)
	for old, new := range map[string]string{
		"/var/lib/otc/" + request:     w.request,
		"/var/lib/otc/update.request": w.updateRequest,
		"/run/otc-update.lock":        w.updateLock,
		"/run/otc-wifi.lock":          w.wifiLock,
		"\nsleep 3\n":                 "\n",
	} {
		if !strings.Contains(body, old) {
			t.Fatalf("%s has no %q", script, old)
		}
		body = strings.ReplaceAll(body, old, new)
	}
	w.script = filepath.Join(dir, script)
	if err := os.WriteFile(w.script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\ncase \"$1\" in\n  is-active) cat \"$FAKE_DIR/unit\" 2>/dev/null || echo inactive ;;\n  *) echo \"$*\" >> \"$FAKE_DIR/systemctl.log\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}

	// Nothing in the way: the request goes, and the machine with it.
	w.ask()
	if out := w.run(false); !strings.Contains(out, "as its owner asked from Settings") || w.acted() != action {
		t.Errorf("free: %q, systemctl %q", out, w.acted())
	}
	w.gone()
	// A directory where the request goes: removed all the same.
	if err := os.MkdirAll(filepath.Join(w.request, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if w.run(false); w.acted() != action {
		t.Errorf("a directory as the request: systemctl %q", w.acted())
	}
	w.gone()

	for _, c := range []struct {
		name, said string
		set        func() func()
	}{
		{"an update asked for", "an update was asked for", func() func() {
			if err := os.WriteFile(w.updateRequest, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return func() { os.Remove(w.updateRequest) }
		}},
		{"the update runner at work", "an update is running", func() func() {
			if err := os.WriteFile(filepath.Join(dir, "unit"), []byte("activating\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return func() { os.Remove(filepath.Join(dir, "unit")) }
		}},
		{"update.sh holding its lock", "an update is running", func() func() { return w.hold(w.updateLock, "-s") }},
		{"a Wi-Fi change holding its lock", "the Wi-Fi is being changed from Settings", func() func() { return w.hold(w.wifiLock, "-x") }},
	} {
		undo := c.set()
		w.ask()
		if out := w.run(true); !strings.Contains(out, "dry run: would drop the request: "+c.said) || w.acted() != "" {
			t.Errorf("%s, dry run: %q, systemctl %q", c.name, out, w.acted())
		}
		if _, err := os.Stat(w.request); err != nil {
			t.Errorf("%s: the dry run removed the request", c.name)
		}
		if out := w.run(false); !strings.Contains(out, c.said) || w.acted() != "" {
			t.Errorf("%s: %q, systemctl %q", c.name, out, w.acted())
		}
		w.gone()
		undo()
	}
	w.ask()
	if out := w.run(true); !strings.Contains(out, "dry run: would ") || w.acted() != "" {
		t.Errorf("free, dry run: %q, systemctl %q", out, w.acted())
	}

	// Right after boot: dropped (the runner's MIN_UPTIME, far above any
	// uptime here).
	if err := os.WriteFile(w.script, []byte(strings.Replace(body, "MIN_UPTIME=120", "MIN_UPTIME=999999999", 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := w.run(false); !strings.Contains(out, "up for only") || w.acted() != "" {
		t.Errorf("right after boot: %q, systemctl %q", out, w.acted())
	}
	w.gone()
}

func (w *runnerWorld) ask() {
	if err := os.WriteFile(w.request, []byte("2026-11-04T10:00:00Z\n"), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *runnerWorld) gone() {
	w.t.Helper()
	if _, err := os.Lstat(w.request); err == nil {
		w.t.Error("the request is still there")
	}
}

// run runs the copy (with --dry-run when dry) and returns what it said;
// what it asked systemctl to do is in acted().
func (w *runnerWorld) run(dry bool) string {
	w.t.Helper()
	os.Remove(w.log)
	args := []string{w.script}
	if dry {
		args = append(args, "--dry-run")
	}
	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(w.dir, "bin")+":"+os.Getenv("PATH"), "FAKE_DIR="+w.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("%s: %v: %s", w.script, err, out)
	}
	return string(out)
}

func (w *runnerWorld) acted() string {
	b, _ := os.ReadFile(w.log)
	return strings.TrimSpace(string(b))
}

// hold takes the lock at path (flock -s or -x) in another process until
// the returned func is called.
func (w *runnerWorld) hold(path, mode string) func() {
	w.t.Helper()
	ready := filepath.Join(w.dir, "held")
	os.Remove(ready)
	// --no-fork: one process holds the lock, and killing it frees it.
	cmd := exec.Command("flock", "--no-fork", mode, path, "sh", "-c", `: > "$1"; exec sleep 60`, "sh", ready)
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(ready); err == nil {
			return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	w.t.Fatal("the lock was never taken")
	return nil
}
