// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/binary"
	"testing"
)

// run is the kernel's reading of the program for one call: a classic BPF
// interpreter over struct seccomp_data, for the instructions program uses.
func run(t *testing.T, prog []insn, arch, nr uint32, arg0 uint64) uint32 {
	t.Helper()
	data := make([]byte, 64)
	binary.LittleEndian.PutUint32(data[offNr:], nr)
	binary.LittleEndian.PutUint32(data[offArch:], arch)
	binary.LittleEndian.PutUint64(data[offArg0:], arg0)
	var a uint32
	for pc, steps := 0, 0; ; steps++ {
		if pc >= len(prog) || steps > len(prog) {
			t.Fatalf("the program runs off its end at %d", pc)
		}
		in := prog[pc]
		next := pc + 1
		switch in.code {
		case bpfLdAbs:
			if in.k+4 > uint32(len(data)) {
				t.Fatalf("a load at %d is outside seccomp_data", in.k)
			}
			a = binary.LittleEndian.Uint32(data[in.k:])
		case bpfJeq, bpfJge, bpfJset:
			hit := (in.code == bpfJeq && a == in.k) || (in.code == bpfJge && a >= in.k) || (in.code == bpfJset && a&in.k != 0)
			if hit {
				next += int(in.jt)
			} else {
				next += int(in.jf)
			}
		case bpfRet:
			return in.k
		default:
			t.Fatalf("instruction %#x at %d", in.code, pc)
		}
		pc = next
	}
}

func TestFilterRefusesWhatSysAdminWouldOpen(t *testing.T) {
	prog, err := program()
	if err != nil {
		t.Fatal(err)
	}
	const deny, enosys, allow = retErrno | ePerm, retErrno | eNoSys, uint32(retAllow)
	// Docker's profile's CAP_SYS_ADMIN group, by name, on both.
	sysAdmin := []string{"bpf", "fanotify_init", "fsconfig", "fsmount", "fsopen", "fspick", "lookup_dcookie",
		"mount", "mount_setattr", "move_mount", "open_tree", "perf_event_open", "quotactl", "quotactl_fd",
		"setdomainname", "sethostname", "setns", "syslog", "umount2", "unshare"}
	for _, a := range []struct {
		name          string
		arch          uint32
		denied        []syscallNo
		clone, clone3 uint32
		// read, write, openat, execve, futex, mmap, exit_group, prctl,
		// seccomp, ptrace, clock_nanosleep: what the device and ffmpeg use.
		allowed []uint32
	}{
		{"x86_64", auditArchX8664, deniedX8664, cloneX8664, clone3X8664, []uint32{0, 1, 257, 59, 202, 9, 231, 157, 317, 101, 230}},
		{"aarch64", auditArchAarch64, deniedAarch64, cloneAarch64, clone3Aarch64, []uint32{63, 64, 56, 221, 98, 222, 94, 167, 277, 117, 115}},
	} {
		names := map[string]bool{}
		for _, d := range a.denied {
			if names[d.name] {
				t.Errorf("%s: %s twice", a.name, d.name)
			}
			names[d.name] = true
			if got := run(t, prog, a.arch, d.nr, 0); got != deny {
				t.Errorf("%s: %s (%d) answers %#x, not EPERM", a.name, d.name, d.nr, got)
			}
		}
		for _, n := range sysAdmin {
			if !names[n] {
				t.Errorf("%s: %s, which Docker's profile allows only with CAP_SYS_ADMIN, isn't refused", a.name, n)
			}
		}
		for _, nr := range a.allowed {
			if got := run(t, prog, a.arch, nr, 0); got != allow {
				t.Errorf("%s: call %d answers %#x, not allowed", a.name, nr, got)
			}
		}
		// clone: a thread or a process, allowed; any namespace, refused.
		thread := uint64(0x3d0f00) // CLONE_VM|FS|FILES|SIGHAND|THREAD|SYSVSEM|SETTLS|PARENT_SETTID|CHILD_CLEARTID
		for _, flags := range []uint64{thread, 0x11 /* SIGCHLD */, 0x4111 /* CLONE_VFORK|CLONE_VM|SIGCHLD */} {
			if got := run(t, prog, a.arch, a.clone, flags); got != allow {
				t.Errorf("%s: clone(%#x) answers %#x", a.name, flags, got)
			}
		}
		for _, flag := range []uint64{0x10000000, 0x40000000, 0x20000000, 0x08000000, 0x04000000, 0x02000000, 0x00020000} {
			if got := run(t, prog, a.arch, a.clone, thread|flag|0x11); got != deny {
				t.Errorf("%s: clone with %#x answers %#x, not EPERM", a.name, flag, got)
			}
		}
		if got := run(t, prog, a.arch, a.clone3, 0); got != enosys {
			t.Errorf("%s: clone3 answers %#x, not ENOSYS", a.name, got)
		}
	}
	// Another architecture's calls (32-bit compat) and x32: refused, any.
	for _, arch := range []uint32{0x40000003 /* i386 */, 0x40000028 /* arm */, 0} {
		for _, nr := range []uint32{0, 1, 3, 120, 435} {
			if got := run(t, prog, arch, nr, 0); got != deny {
				t.Errorf("arch %#x call %d answers %#x", arch, nr, got)
			}
		}
	}
	if got := run(t, prog, auditArchX8664, x32SyscallBit|1, 0); got != deny {
		t.Errorf("an x32 write answers %#x", got)
	}
}

func TestAssembleRefusesWhatBPFCant(t *testing.T) {
	if _, err := assemble([]op{{code: bpfJeq, jt: "nowhere"}, {code: bpfRet}}); err == nil {
		t.Error("a jump to no label")
	}
	if _, err := assemble([]op{{label: "a", code: bpfRet}, {label: "a", code: bpfRet}}); err == nil {
		t.Error("a label twice")
	}
	if _, err := assemble([]op{{label: "back", code: bpfRet}, {code: bpfJeq, jt: "back"}}); err == nil {
		t.Error("a jump back")
	}
	far := []op{{code: bpfJeq, jt: "end"}}
	for i := 0; i < 300; i++ {
		far = append(far, op{code: bpfRet})
	}
	far = append(far, op{label: "end", code: bpfRet})
	if _, err := assemble(far); err == nil {
		t.Error("a jump past 255")
	}
}
