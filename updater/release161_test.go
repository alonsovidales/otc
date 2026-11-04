// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The stand-ins for apt-get, dpkg and systemctl: each writes its arguments
// to CALLS, apt-get with the DEBIAN_FRONTEND it was given; apt-get install
// fails always (APT=broken), only the first time (APT=once) or never.
const (
	fakeAptGet = `#!/bin/bash
echo "apt-get DEBIAN_FRONTEND=${DEBIAN_FRONTEND:-} $*" >>"$CALLS"
case " $* " in
    *" install "*)
        if [ "$APT" = broken ]; then echo "E: dpkg was interrupted" >&2; exit 100; fi
        if [ "$APT" = once ] && [ ! -e "$CALLS.failed" ]; then : >"$CALLS.failed"; exit 100; fi
        ;;
esac
exit 0
`
	fakeLogged = `#!/bin/bash
echo "$(basename "$0") $*" >>"$CALLS"
exit 0
`
)

// release161 runs updates/corrected/161.sh with its paths under root (a
// new temporary one when ""), the package tools stood in for, and nft there
// or not.
func release161(t *testing.T, root, apt string, nft bool) (string, string, string) {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	body := readFile(t, "../scripts/updates/corrected/161.sh")
	body = strings.ReplaceAll(body, "/etc/", root+"/etc/")
	body = strings.ReplaceAll(body, "/usr/sbin/nft", root+"/usr/sbin/nft")
	script := filepath.Join(root, "161.sh")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{bin, root + "/etc/apt/apt.conf.d", root + "/etc/systemd/system", root + "/usr/sbin"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{script: body, bin + "/apt-get": fakeAptGet, bin + "/dpkg": fakeLogged, bin + "/systemctl": fakeLogged}
	if nft {
		files[root+"/usr/sbin/nft"] = fakeLogged
	}
	for path, text := range files {
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if !nft {
		_ = os.Remove(root + "/usr/sbin/nft")
	}
	calls := filepath.Join(root, "calls")
	_ = os.Remove(calls)
	_ = os.Remove(calls + ".failed")
	cmd := exec.Command("/bin/bash", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CALLS="+calls, "APT="+apt, "OTC_CONTAINER=", "DEBIAN_FRONTEND=")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("161.sh failed (it must never stop the update): %v\n%s", err, b)
	}
	return root, readFile(t, calls), string(b)
}

// Release 161 stopped every later update on a device whose apt failed. As
// corrected, it asks nothing (no debconf question, a changed configuration
// file kept, the lock waited for), finishes what dpkg left, tries a failed
// install once more, sets up all it can - the IPv4-options filter wherever
// nft is - and only then reports what is missing, by the mark every later
// update runs it again for, never by stopping the update.
func TestCorrected161NeverStopsTheUpdate(t *testing.T) {
	pending := func(root string) bool {
		_, err := os.Stat(root + "/etc/otc/os-updates.pending")
		return err == nil
	}

	root, calls, out := release161(t, "", "broken", true)
	if !pending(root) {
		t.Errorf("apt failing: no os-updates.pending\n%s", out)
	}
	for _, want := range []string{
		"dpkg --force-confdef --force-confold --configure -a",
		"apt-get DEBIAN_FRONTEND=noninteractive -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=600 install unattended-upgrades nftables",
		"systemctl restart otc-ipv4-options.service",
	} {
		if !strings.Contains(calls, want+"\n") {
			t.Errorf("apt failing: %q was never run:\n%s", want, calls)
		}
	}
	if n := strings.Count(calls, " install unattended-upgrades nftables"); n != 2 {
		t.Errorf("apt failing: the install was tried %d times, want 2", n)
	}
	for _, f := range []string{"/etc/apt/apt.conf.d/20auto-upgrades", "/etc/apt/apt.conf.d/52otc-security-updates", "/etc/otc/ipv4-options.nft", "/etc/systemd/system/otc-ipv4-options.service"} {
		if _, err := os.Stat(root + f); err != nil {
			t.Errorf("apt failing: %s was not written: %v", f, err)
		}
	}

	root, calls, out = release161(t, "", "once", true)
	if pending(root) || strings.Count(calls, " install unattended-upgrades nftables") != 2 {
		t.Errorf("apt failing once: pending %v after\n%s\n%s", pending(root), calls, out)
	}

	root, _, _ = release161(t, "", "fine", true)
	if pending(root) {
		t.Error("all in place, yet os-updates.pending")
	}

	root, calls, _ = release161(t, "", "fine", false)
	if !pending(root) || strings.Contains(calls, "systemctl restart otc-ipv4-options.service") {
		t.Errorf("no nft: pending %v, calls\n%s", pending(root), calls)
	}

	// The mark an earlier run left goes once everything is in place.
	root, _, _ = release161(t, "", "broken", true)
	if !pending(root) {
		t.Fatal("no mark to clear")
	}
	if release161(t, root, "fine", true); pending(root) {
		t.Error("everything in place at the next run, yet the mark stayed")
	}
}
