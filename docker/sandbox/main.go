// SPDX-License-Identifier: AGPL-3.0-or-later

// otc-sandbox runs a command under a system-call filter of its own. The
// Docker image's entrypoint starts the device's process through it, before
// setpriv makes it the otc user (docker/entrypoint.sh, start_app):
//
//	otc-sandbox <command> [arguments...]
//
// In mirror mode (docker/compose.mirror.yaml) the container holds
// CAP_SYS_ADMIN for the mirror's root helper, and Docker's seccomp profile
// then lets every process in it create user namespaces and call mount and
// bpf - the device's too, though it runs with no capabilities: code run in
// it (a media parser's bug) could take capabilities inside a namespace of
// its own and reach kernel code an unprivileged process never should. The
// filter (filter.go) refuses those calls again, in either mode, and the
// command, everything it starts and every later filter inherit it; it can't
// be taken off.
//
// Before installing it, a copy of this program installs it and checks it
// (--probe): a machine where it can't be installed, or where it would
// refuse more than it should (an emulator that runs system calls another
// way), runs the command without it, saying so on stderr - the device must
// start whatever the host. Exit status 127 when the command isn't found,
// 126 when it can't be run.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const probeArg = "--probe"

func init() {
	// The filter and no_new_privs are set thread by thread: both on the
	// thread this runs on (install syncs the filter to the others).
	runtime.LockOSThread()
}

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == probeArg {
		os.Exit(probe())
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: otc-sandbox <command> [arguments...]")
		os.Exit(2)
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "otc-sandbox: %v\n", err)
		os.Exit(127)
	}
	if err := checked(); err != nil {
		fmt.Fprintf(os.Stderr, "otc-sandbox: WARNING: no system-call filter on this machine (%v): %s runs without it\n", err, args[0])
	} else if err := apply(); err != nil {
		fmt.Fprintf(os.Stderr, "otc-sandbox: WARNING: the system-call filter could not be installed (%v): %s runs without it\n", err, args[0])
	}
	err = syscall.Exec(path, args, os.Environ())
	fmt.Fprintf(os.Stderr, "otc-sandbox: %s: %v\n", path, err)
	os.Exit(126)
}

// apply installs the filter on this process.
func apply() error {
	prog, err := program()
	if err != nil {
		return err
	}
	return install(prog)
}

// checked runs a copy of this program with --probe, given up after 10 s:
// nil when the filter installs there and does what it should.
func checked() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, self, probeArg).CombinedOutput()
	said := strings.TrimSpace(string(out))
	if err == nil && said == "ok" {
		return nil
	}
	if said == "" {
		said = fmt.Sprint(err)
	}
	return fmt.Errorf("its check said: %s", said)
}

// probe installs the filter and checks it: a call it refuses answers
// EPERM, one it allows still works. "ok" on stdout, exit 0.
func probe() int {
	if err := apply(); err != nil {
		fmt.Println(err)
		return 3
	}
	if err := refused(); err != nil {
		fmt.Println(err)
		return 4
	}
	if _, err := os.Stat("/"); err != nil {
		fmt.Println("an allowed call failed:", err)
		return 5
	}
	fmt.Println("ok")
	return 0
}
