// SPDX-License-Identifier: AGPL-3.0-or-later

// otc-mirror builds, mounts and watches the two-disk mirror of the Docker
// image's mirror mode (docker/compose.mirror.yaml), as scripts/install.sh
// and scripts/raid_watch.py do on a Raspberry Pi. The entrypoint runs it as
// root inside the container:
//
//	otc-mirror prepare [--no-create]   find, assemble or build the mirror, mount it
//	                                   on /mnt/storage, add a replacement disk
//	otc-mirror watch --array mdN       log its state, grow it onto bigger disks
//	otc-mirror release --array mdN     unmount and stop it
//
// The disks are /dev/otc-disk1 and /dev/otc-disk2 (OTC_MIRROR_DEV1/2 for a
// test), named after OTC_MIRROR_DISK1/2; OTC_MIRROR_ERASE lists the ones
// that may be erased. A disk is known by its major:minor, never by a name,
// and one that anything on this server holds is never touched. prepare
// prints array=, dev1=, dev2=, label1=, label2= and uuid= lines to stdout.
// What it does goes to stderr and to the storage log; exit status 1 when it
// refuses or fails, with what to do.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "otc-mirror runs as root: the container's entrypoint starts it")
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], newHost()))
}

func run(args []string, h *host) int {
	if len(args) == 0 {
		return usage(h.stderr)
	}
	fs := flag.NewFlagSet("otc-mirror "+args[0], flag.ContinueOnError)
	fs.SetOutput(h.stderr)
	noCreate := fs.Bool("no-create", false, "assemble and mount only")
	md := fs.String("array", "", "the mirror's md device (mdN)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return usage(h.stderr)
	}
	switch args[0] {
	case "prepare":
		if *md != "" {
			return usage(h.stderr)
		}
		if err := h.prepare(*noCreate); err != nil {
			h.say("%v", err)
			return 1
		}
	case "watch", "release":
		if !mdName.MatchString(*md) || *noCreate {
			return usage(h.stderr)
		}
		if args[0] == "release" {
			h.release(*md)
			break
		}
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			<-sig
			os.Exit(0)
		}()
		h.watch(*md)
	default:
		return usage(h.stderr)
	}
	return 0
}

func usage(w io.Writer) int {
	fmt.Fprintln(w, "usage: otc-mirror prepare [--no-create] | watch --array mdN | release --array mdN")
	return 1
}

// newHost is the real system, set up from the environment.
func newHost() *host {
	h := &host{
		sys: "/sys", dev: "/dev", proc: "/proc", mount: "/mnt/storage",
		erase:  strings.Fields(os.Getenv("OTC_MIRROR_ERASE")),
		expect: strings.TrimSpace(os.Getenv("OTC_MIRROR_UUID")),
		run:    runCommand, devOf: devOf, free: free, readEnds: readEnds, mknod: mknod,
		sync:   func() { syncFS("/mnt/storage") },
		stdout: os.Stdout, stderr: os.Stderr,
	}
	h.log = &storageLog{path: storageLogPath, max: storageLogMax, now: time.Now, stderr: os.Stderr}
	for i := range h.disks {
		n := i + 1
		path := fmt.Sprintf("/dev/otc-disk%d", n)
		// only a path: the device process gets OTC_MIRROR_DEV1/2 as major:minor
		if p := os.Getenv(fmt.Sprintf("OTC_MIRROR_DEV%d", n)); strings.HasPrefix(p, "/") {
			path = p
		}
		name := strings.TrimSpace(os.Getenv(fmt.Sprintf("OTC_MIRROR_DISK%d", n)))
		h.disks[i] = disk{n: n, path: path, name: name, label: labelOf(name, n)}
	}
	return h
}

func runCommand(name string, args ...string) result {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{out: out.String(), err: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		r.code, r.err = -1, err.Error()
	}
	return r
}

func devOf(path string) (string, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return "", err
	}
	if uint32(st.Mode)&unix.S_IFMT != unix.S_IFBLK {
		return "", fmt.Errorf("%s is not a block device", path)
	}
	return fmt.Sprintf("%d:%d", unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev))), nil
}

// free: the kernel lets this process hold the device alone - it refuses
// (EBUSY) while it is mounted, swapped on or held by md or dm, anywhere on
// the machine.
func free(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_EXCL|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func mknod(path, id string) error {
	maj, mnr, _ := strings.Cut(id, ":")
	a, err1 := strconv.ParseUint(maj, 10, 32)
	b, err2 := strconv.ParseUint(mnr, 10, 32)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("bad device number %q", id)
	}
	return unix.Mknod(path, unix.S_IFBLK|0o600, int(unix.Mkdev(uint32(a), uint32(b))))
}
