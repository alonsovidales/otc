// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && (amd64 || arm64)

package main

import (
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

// The filter's numbers for the architecture this test runs on are
// x/sys/unix's (run on arm64 and amd64, each table is checked).
func TestSyscallNumbersAreTheKernels(t *testing.T) {
	want := map[string]uintptr{
		"bpf": unix.SYS_BPF, "fanotify_init": unix.SYS_FANOTIFY_INIT, "fsconfig": unix.SYS_FSCONFIG,
		"fsmount": unix.SYS_FSMOUNT, "fsopen": unix.SYS_FSOPEN, "fspick": unix.SYS_FSPICK,
		"lookup_dcookie": unix.SYS_LOOKUP_DCOOKIE, "mount": unix.SYS_MOUNT, "mount_setattr": unix.SYS_MOUNT_SETATTR,
		"move_mount": unix.SYS_MOVE_MOUNT, "open_tree": unix.SYS_OPEN_TREE, "perf_event_open": unix.SYS_PERF_EVENT_OPEN,
		"quotactl": unix.SYS_QUOTACTL, "quotactl_fd": unix.SYS_QUOTACTL_FD, "setdomainname": unix.SYS_SETDOMAINNAME,
		"sethostname": unix.SYS_SETHOSTNAME, "setns": unix.SYS_SETNS, "syslog": unix.SYS_SYSLOG,
		"umount2": unix.SYS_UMOUNT2, "unshare": unix.SYS_UNSHARE, "pivot_root": unix.SYS_PIVOT_ROOT,
		"add_key": unix.SYS_ADD_KEY, "keyctl": unix.SYS_KEYCTL, "request_key": unix.SYS_REQUEST_KEY,
		"name_to_handle_at": unix.SYS_NAME_TO_HANDLE_AT, "open_by_handle_at": unix.SYS_OPEN_BY_HANDLE_AT,
		"init_module": unix.SYS_INIT_MODULE, "finit_module": unix.SYS_FINIT_MODULE, "delete_module": unix.SYS_DELETE_MODULE,
	}
	table, clone, clone3 := deniedX8664, cloneX8664, clone3X8664
	if runtime.GOARCH == "arm64" {
		table, clone, clone3 = deniedAarch64, cloneAarch64, clone3Aarch64
	}
	if len(table) != len(want) {
		t.Errorf("%d calls refused, %d checked", len(table), len(want))
	}
	for _, d := range table {
		if w, ok := want[d.name]; !ok || uintptr(d.nr) != w {
			t.Errorf("%s: %d in the table, %d in x/sys/unix (%v)", d.name, d.nr, w, ok)
		}
	}
	if uintptr(clone) != unix.SYS_CLONE || uintptr(clone3) != unix.SYS_CLONE3 {
		t.Errorf("clone %d/%d, clone3 %d/%d", clone, unix.SYS_CLONE, clone3, unix.SYS_CLONE3)
	}
}
