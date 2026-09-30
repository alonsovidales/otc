// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"golang.org/x/sys/unix"
)

// Issue #156: the heap holds decrypted files and the session keys, and
// processing writes decrypted media to temporary files for ffmpeg. None of
// it may reach the SD card, so the memory is locked (swap can't write it
// out; the image's swap is also zram only, see install.sh) and the
// temporary files live on a RAM filesystem.

// lockMemory locks this process's memory, as it is touched. [otc]
// disable-mlock turns it off, e.g. on a development machine without the
// unit's LimitMEMLOCK=infinity.
func lockMemory() {
	if cfg.GetBool("otc", "disable-mlock") {
		return
	}
	if err := unix.Mlockall(unix.MCL_CURRENT | unix.MCL_FUTURE | unix.MCL_ONFAULT); err != nil {
		log.Error("could not lock memory, decrypted data could be swapped out (needs LimitMEMLOCK=infinity):", err)
		return
	}
	log.Info("memory locked, nothing of it can be swapped out")
}

// secureTempDir points TMPDIR at a directory of this process's own on a
// RAM filesystem - /tmp when it is one (the service's PrivateTmp on a
// tmpfs /tmp), otherwise /dev/shm - and removes what processes that no
// longer run left behind (an OOM kill skips every deferred Remove).
func secureTempDir() {
	var base string
	for _, dir := range []string{os.TempDir(), "/tmp", "/dev/shm"} {
		var st unix.Statfs_t
		if unix.Statfs(dir, &st) == nil && st.Type == unix.TMPFS_MAGIC {
			base = dir
			break
		}
	}
	if base == "" {
		log.Error("no RAM filesystem for temporary files: decrypted media being processed is written to", os.TempDir())
		return
	}
	sweepTempDirs(base)
	dir := filepath.Join(base, fmt.Sprintf("otc-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Error("could not create the temporary directory", dir, ":", err)
		return
	}
	os.Setenv("TMPDIR", dir)
}

// sweepTempDirs removes the otc-<pid> directories of processes that are
// gone, and loose otc-* files from before these directories existed.
func sweepTempDirs(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "otc-") {
			continue
		}
		if e.IsDir() {
			pid, err := strconv.Atoi(strings.TrimPrefix(name, "otc-"))
			if err != nil || pid == os.Getpid() || syscall.Kill(pid, 0) != syscall.ESRCH {
				continue
			}
		}
		os.RemoveAll(filepath.Join(base, name))
	}
}
