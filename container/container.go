// SPDX-License-Identifier: AGPL-3.0-or-later

// Package container is what changes when the device runs from its Docker
// image (docker/, beta) instead of on a machine of its own. The image sets
// OTC_CONTAINER=1; nothing else does - not install.sh, not a Raspberry Pi -
// and every caller keeps its usual behaviour while Enabled is false.
//
// Inside a container the machine's own files tell the host's story: its
// memory (/proc/meminfo), its disk arrays (/proc/mdstat), its CPUs. This
// package answers for the container instead - its memory limit, whether a
// release's image has been published yet - and holds the rules for the
// first owner password, which a container may be asked for from anywhere
// its port reaches.
package container

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// EnvVar switches container mode on: "1" (the image's ENV).
const EnvVar = "OTC_CONTAINER"

// EnvSetupUntil is until when the first owner password may be chosen from
// the web app, in Unix seconds: the image's start-up script writes it, 30
// minutes from the container's first start, and keeps it across restarts.
const EnvSetupUntil = "OTC_SETUP_UNTIL"

// Enabled reports whether this process runs from the container image.
// Read each time, not once: it is one environment lookup, and tests set
// it per case.
func Enabled() bool {
	return os.Getenv(EnvVar) == "1"
}

// cgnat is 100.64.0.0/10, the shared address space carrier-grade NAT and
// Tailscale hand out: reachable only from inside one's own network.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// HomeAddress reports whether addr (an IP address, as a connection's peer
// shows it) is one of the home network's own: loopback, the private IPv4
// ranges (RFC 1918), IPv6 unique local addresses (fc00::/7) or
// 100.64.0.0/10. An IPv4 address written as IPv6 (::ffff:a.b.c.d) counts as
// the IPv4 address it is. Anything that isn't an address is not one.
func HomeAddress(addr string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || cgnat.Contains(a)
}

// SetupWindowOpen reports whether the first owner password may still be
// chosen from the web app at now: before EnvSetupUntil. Missing or not a
// number, the window counts as closed - the password is then set on the
// server (otc-entrypoint set-password).
func SetupWindowOpen(now time.Time) bool {
	return setupWindowOpen(os.Getenv(EnvSetupUntil), now)
}

func setupWindowOpen(until string, now time.Time) bool {
	end, err := strconv.ParseInt(strings.TrimSpace(until), 10, 64)
	if err != nil || end <= 0 {
		return false
	}
	return now.Unix() < end
}

// shareDir is where the image keeps what it was built as: release (the
// release number) and revision (the commit).
var shareDir = "/usr/share/otc"

// Platform is the support header's line for this process: "docker", the
// image's release, the CPU architecture and the image's revision.
func Platform() string {
	return fmt.Sprintf("docker %s %s %s",
		imageFact(filepath.Join(shareDir, "release")),
		runtime.GOARCH,
		imageFact(filepath.Join(shareDir, "revision")))
}

// imageFact is the first line of one of the image's own files, as plain
// text, or "unknown".
func imageFact(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	line = strings.TrimSpace(line)
	if line == "" || len(line) > 64 || strings.IndexFunc(line, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '_')
	}) >= 0 {
		return "unknown"
	}
	return line
}
