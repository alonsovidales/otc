// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"unsafe"

	"golang.org/x/sys/unix"
)

// readEnds reads the device's first and last MiB (O_DIRECT, so a read
// error isn't hidden by the page cache).
func readEnds(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECT|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	size, err := unix.Seek(fd, 0, io.SeekEnd)
	if err != nil {
		return err
	}
	const span = 1 << 20
	// O_DIRECT wants an aligned buffer: take an aligned slice of a bigger one.
	raw := make([]byte, span+4096)
	off := 4096 - int(uintptr(unsafe.Pointer(&raw[0]))&4095)
	buf := raw[off : off+span]
	for _, at := range []int64{0, max(0, (size-span)&^4095)} {
		if _, err := unix.Pread(fd, buf, at); err != nil {
			return fmt.Errorf("reading at %d: %w", at, err)
		}
	}
	return nil
}

// syncFS flushes the filesystem mounted at dir before it is unmounted - that
// one only: a sync(2) of everything could wait on any of the host's.
func syncFS(dir string) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	_ = unix.Syncfs(fd)
	_ = unix.Close(fd)
}
