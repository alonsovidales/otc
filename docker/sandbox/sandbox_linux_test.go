// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The filter on a real kernel: this test binary run again with
// OTC_SANDBOX_TEST set installs it and reports what each call answered.
func TestMain(m *testing.M) {
	if what := os.Getenv("OTC_SANDBOX_TEST"); what != "" {
		os.Exit(filtered(what))
	}
	os.Exit(m.Run())
}

func filtered(what string) int {
	if what == "probe" {
		return probe()
	}
	if err := apply(); err != nil {
		fmt.Println("install:", err)
		return 3
	}
	say := func(name string, err error) { fmt.Printf("%s=%v\n", name, errName(err)) }
	say("unshare-user", unix.Unshare(unix.CLONE_NEWUSER))
	say("unshare-net", unix.Unshare(unix.CLONE_NEWNET))
	say("setns", unix.Setns(-1, 0))
	_, _, e := unix.Syscall(unix.SYS_CLONE3, 0, 0, 0)
	say("clone3", errnoErr(e))
	// A child in a user namespace of its own: clone with CLONE_NEWUSER.
	child := exec.Command("/bin/true")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}}
	say("clone-newuser", child.Run())
	_, _, e = unix.Syscall(unix.SYS_BPF, 0, 0, 0)
	say("bpf", errnoErr(e))
	say("mount", unix.Mount("none", os.TempDir(), "tmpfs", 0, ""))
	// What must still work: a process started (clone without a new
	// namespace), a program whose threads glibc starts (clone3, ENOSYS,
	// then clone), a pipe between two.
	say("exec", exec.Command("/bin/true").Run())
	if py, err := exec.LookPath("python3"); err == nil {
		say("threads", exec.Command(py, "-I", "-c", "import threading\nt = threading.Thread(target=print, args=('x',))\nt.start(); t.join()").Run())
	} else {
		say("threads", nil)
	}
	say("pipe", exec.Command("/bin/sh", "-c", "echo ok | cat >/dev/null").Run())
	return 0
}

func errnoErr(e syscall.Errno) error {
	if e == 0 {
		return nil
	}
	return e
}

func errName(err error) string {
	var e syscall.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &e):
		return unix.ErrnoName(e)
	default:
		return err.Error()
	}
}

func underFilter(t *testing.T, what string) map[string]string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	cmd.Env = append(os.Environ(), "OTC_SANDBOX_TEST="+what)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		} else {
			got[line] = ""
		}
	}
	return got
}

func TestFilterOnThisKernel(t *testing.T) {
	got := underFilter(t, "calls")
	for name, want := range map[string]string{
		"unshare-user": "EPERM", "unshare-net": "EPERM", "setns": "EPERM", "clone3": "ENOSYS",
		"clone-newuser": "EPERM", "bpf": "EPERM", "mount": "EPERM",
		"exec": "ok", "threads": "ok", "pipe": "ok",
	} {
		if got[name] != want {
			t.Errorf("%s: %q, want %q (all: %v)", name, got[name], want, got)
		}
	}
}

func TestProbeSaysOK(t *testing.T) {
	if got := underFilter(t, "probe"); len(got) != 1 || got["ok"] != "" {
		t.Errorf("the probe said %v", got)
	}
}
