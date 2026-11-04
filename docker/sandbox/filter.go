// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "fmt"

// The filter, as a classic BPF program over struct seccomp_data (the
// system call's number at offset 0, its architecture at 4, its arguments
// from 16, eight bytes each, the low half first: little-endian only, as
// both architectures the image is built for are).
//
// What it refuses is what Docker's default seccomp profile refuses a
// container that lacks CAP_SYS_ADMIN - new namespaces (unshare, setns,
// clone with a CLONE_NEW* flag; clone3, which a filter can't read the
// flags of, answers ENOSYS, and glibc falls back to clone), mount and its
// newer calls, bpf, perf_event_open, quotactl, fanotify_init, syslog,
// lookup_dcookie, sethostname, setdomainname - plus pivot_root, the
// kernel's keyring, file handles and module loading, which the default
// profile refuses too. Every other call is allowed, as the profile allows
// it. So the device's process gets in mirror mode (compose.mirror.yaml adds
// SYS_ADMIN for the mirror's root helper) what it gets without it.
//
// Both architectures' numbers are in the one program: an amd64 image run
// under Rosetta on an arm64 Linux makes arm64 system calls, which the
// filter then sees as such. Calls of any other architecture (a 32-bit
// compat call, x32) are refused.

const (
	auditArchX8664   = 0xc000003e
	auditArchAarch64 = 0xc00000b7
	x32SyscallBit    = 0x40000000
	// CLONE_NEWNS | CLONE_NEWCGROUP | CLONE_NEWUTS | CLONE_NEWIPC |
	// CLONE_NEWUSER | CLONE_NEWPID | CLONE_NEWNET: the mask Docker's profile
	// checks clone's flags against.
	cloneNewFlags = 0x7e020000

	retAllow = 0x7fff0000
	retErrno = 0x00050000
	ePerm    = 1
	eNoSys   = 38

	offNr    = 0
	offArch  = 4
	offArg0  = 16
	bpfLdAbs = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfJeq   = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfJge   = 0x35 // BPF_JMP | BPF_JGE | BPF_K
	bpfJset  = 0x45 // BPF_JMP | BPF_JSET | BPF_K
	bpfRet   = 0x06 // BPF_RET | BPF_K
)

// syscallNo is one refused call and its number on an architecture.
type syscallNo struct {
	name string
	nr   uint32
}

// The calls refused outright, by architecture (x/sys/unix's numbers;
// tables_linux_test.go checks each against them on its own architecture).
var deniedX8664 = []syscallNo{
	{"bpf", 321}, {"fanotify_init", 300}, {"fsconfig", 431}, {"fsmount", 432}, {"fsopen", 430},
	{"fspick", 433}, {"lookup_dcookie", 212}, {"mount", 165}, {"mount_setattr", 442}, {"move_mount", 429},
	{"open_tree", 428}, {"perf_event_open", 298}, {"quotactl", 179}, {"quotactl_fd", 443},
	{"setdomainname", 171}, {"sethostname", 170}, {"setns", 308}, {"syslog", 103}, {"umount2", 166},
	{"unshare", 272}, {"pivot_root", 155}, {"add_key", 248}, {"keyctl", 250}, {"request_key", 249},
	{"name_to_handle_at", 303}, {"open_by_handle_at", 304}, {"init_module", 175}, {"finit_module", 313},
	{"delete_module", 176},
}

var deniedAarch64 = []syscallNo{
	{"bpf", 280}, {"fanotify_init", 262}, {"fsconfig", 431}, {"fsmount", 432}, {"fsopen", 430},
	{"fspick", 433}, {"lookup_dcookie", 18}, {"mount", 40}, {"mount_setattr", 442}, {"move_mount", 429},
	{"open_tree", 428}, {"perf_event_open", 241}, {"quotactl", 60}, {"quotactl_fd", 443},
	{"setdomainname", 162}, {"sethostname", 161}, {"setns", 268}, {"syslog", 116}, {"umount2", 39},
	{"unshare", 97}, {"pivot_root", 41}, {"add_key", 217}, {"keyctl", 219}, {"request_key", 218},
	{"name_to_handle_at", 264}, {"open_by_handle_at", 265}, {"init_module", 105}, {"finit_module", 273},
	{"delete_module", 106},
}

// clone and clone3 by architecture: clone is allowed without a CLONE_NEW*
// flag, clone3 answers ENOSYS.
var (
	cloneX8664, clone3X8664     uint32 = 56, 435
	cloneAarch64, clone3Aarch64 uint32 = 220, 435
)

// insn is one BPF instruction (struct sock_filter).
type insn struct {
	code   uint16
	jt, jf uint8
	k      uint32
}

// op is an instruction whose jumps name labels; assemble resolves them.
type op struct {
	label  string // this instruction's own label, if any
	code   uint16
	jt, jf string // labels jumped to ("" = the next instruction)
	k      uint32
}

// program builds the filter.
func program() ([]insn, error) {
	ops := []op{
		{code: bpfLdAbs, k: offArch},
		{code: bpfJeq, k: auditArchX8664, jt: "x86_64"},
		{code: bpfJeq, k: auditArchAarch64, jt: "aarch64", jf: "deny"},
	}
	arch := func(name string, clone, clone3 uint32, denied []syscallNo, x32 bool) {
		ops = append(ops, op{label: name, code: bpfLdAbs, k: offNr})
		if x32 {
			ops = append(ops, op{code: bpfJge, k: x32SyscallBit, jt: "deny"})
		}
		ops = append(ops,
			op{code: bpfJeq, k: clone, jt: "clone"},
			op{code: bpfJeq, k: clone3, jt: "enosys"})
		for _, d := range denied {
			ops = append(ops, op{code: bpfJeq, k: d.nr, jt: "deny"})
		}
		ops = append(ops, op{code: bpfRet, k: retAllow})
	}
	arch("x86_64", cloneX8664, clone3X8664, deniedX8664, true)
	arch("aarch64", cloneAarch64, clone3Aarch64, deniedAarch64, false)
	ops = append(ops,
		op{label: "clone", code: bpfLdAbs, k: offArg0},
		op{code: bpfJset, k: cloneNewFlags, jt: "deny"},
		op{code: bpfRet, k: retAllow},
		op{label: "enosys", code: bpfRet, k: retErrno | eNoSys},
		op{label: "deny", code: bpfRet, k: retErrno | ePerm})
	return assemble(ops)
}

// assemble resolves the labels into BPF's forward, 8-bit relative jumps.
func assemble(ops []op) ([]insn, error) {
	at := map[string]int{}
	for i, o := range ops {
		if o.label != "" {
			if _, dup := at[o.label]; dup {
				return nil, fmt.Errorf("label %q twice", o.label)
			}
			at[o.label] = i
		}
	}
	jump := func(from int, label string) (uint8, error) {
		if label == "" {
			return 0, nil
		}
		to, ok := at[label]
		if !ok {
			return 0, fmt.Errorf("no label %q", label)
		}
		d := to - from - 1
		if d < 0 || d > 255 {
			return 0, fmt.Errorf("jump from %d to %q (%d) out of reach", from, label, to)
		}
		return uint8(d), nil
	}
	out := make([]insn, len(ops))
	for i, o := range ops {
		jt, err := jump(i, o.jt)
		if err != nil {
			return nil, err
		}
		jf, err := jump(i, o.jf)
		if err != nil {
			return nil, err
		}
		out[i] = insn{code: o.code, jt: jt, jf: jf, k: o.k}
	}
	if len(out) > 4096 {
		return nil, fmt.Errorf("%d instructions: more than BPF takes", len(out))
	}
	return out, nil
}
