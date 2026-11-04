// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// install sets no_new_privs (a filter needs it without CAP_SYS_ADMIN, and
// the device runs with it anyway: setpriv --no-new-privs) and installs prog
// on every thread of this process (SECCOMP_FILTER_FLAG_TSYNC).
func install(prog []insn) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	filters := make([]unix.SockFilter, len(prog))
	for i, in := range prog {
		filters[i] = unix.SockFilter{Code: in.code, Jt: in.jt, Jf: in.jf, K: in.k}
	}
	fprog := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	r, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC,
		uintptr(unsafe.Pointer(&fprog)))
	if errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	if r != 0 {
		return fmt.Errorf("seccomp: thread %d could not take the filter", r)
	}
	return nil
}

// refused checks that a call the filter refuses answers EPERM: a new user
// namespace (in a process with threads the kernel would answer EINVAL, so
// EPERM can only be the filter's).
func refused() error {
	err := unix.Unshare(unix.CLONE_NEWUSER)
	if errors.Is(err, unix.EPERM) {
		return nil
	}
	return fmt.Errorf("a new user namespace was not refused (%v)", err)
}
