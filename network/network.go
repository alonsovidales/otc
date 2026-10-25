// SPDX-License-Identifier: AGPL-3.0-or-later

// Package network is Settings > Wi-Fi on the device: the networks it can
// see (ListNetworks), the one it is on now (Current), whether it can change
// its own Wi-Fi at all (Managed) and the change itself (Join, Status).
//
// Reading is unprivileged (nmcli's scan results and device state need no
// special rights), so it runs inside the otc service. Joining a network
// needs root, which the service can't have (NoNewPrivileges): Join hands
// the request to the root runner (scripts/device-runner/
// otc-wifi-join-runner.py, started by otc-wifi-join.path) through a file
// on the /run tmpfs - never the SD card, as it holds the Wi-Fi password -
// and reads the runner's answer back from root's own directory. The
// runner joins the new network on trial, keeps it only once the device
// reaches the internet through it, and otherwise goes back to the one it
// was on.
//
// At level=info nothing here logs a network's name (CLAUDE.md, the log
// rule): Debug only.
package network

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alonsovidales/otc/log"
)

// Network is one name in the scan, all its access points together
// (parseNetworks).
type Network struct {
	SSID   string
	Signal int32
	// Secured: an access point with this name that the runner can join
	// asks for a password.
	Secured bool
	// Current: the device is on it now.
	Current bool
	// Unsupported: every access point with this name is 802.1X
	// (enterprise), WEP or OWE, which the runner can't join.
	Unsupported bool
}

// nmcli runs NetworkManager's command line with args under ctx, in the C
// locale with UTF-8 (names come out as their own bytes). A seam: the
// tests stand in for it.
var nmcli = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "nmcli", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	return cmd.Output()
}

const (
	cListTimeout   = 20 * time.Second
	cQueryTimeout  = 5 * time.Second
	cSecurityWords = "802.1X WEP OWE" // i18n-ignore: nmcli's security tokens
)

// ListNetworks lists the Wi-Fi networks the device sees, one entry per
// name: the current network first, then by signal, then by name. Hidden
// networks (no name) and names that aren't valid UTF-8 (which a protobuf
// string can't carry) are left out. nmcli rescans only when its list is
// old (its default), which NetworkManager also throttles.
func ListNetworks(ctx context.Context) ([]Network, error) {
	ctx, cancel := context.WithTimeout(ctx, cListTimeout)
	defer cancel()
	out, err := nmcli(ctx, "-t", "-f", "ACTIVE,SSID-HEX,SIGNAL,SECURITY", "device", "wifi", "list")
	if err != nil {
		return nil, fmt.Errorf("nmcli device wifi list: %w", err) // i18n-ignore: command-line words and the command's error
	}
	return parseNetworks(out), nil
}

// parseNetworks reads `nmcli -t -f ACTIVE,SSID-HEX,SIGNAL,SECURITY device
// wifi list`. The same name seen on several access points or channels is
// one entry: the strongest signal, current when any of them is the one the
// device is on, secured when any access point the runner can join asks for
// a password - so an open access point copying a secured network's name
// never makes it look open (the runner then refuses to join it open) -
// and unsupported only when the runner can join none of them.
func parseNetworks(out []byte) []Network {
	type seen struct {
		n                                 Network
		anySecured, joinable, joinSecured bool
	}
	best := map[string]*seen{}
	for _, line := range strings.Split(string(out), "\n") {
		f := splitUnescaped(strings.TrimRight(line, "\r"), ':')
		if len(f) < 4 {
			continue
		}
		ssid, ok := decodeSSID(unescape(f[1]))
		if !ok {
			continue
		}
		signal, _ := strconv.ParseInt(unescape(f[2]), 10, 32)
		security := unescape(f[3])
		secured := security != "" && security != "--"
		s := best[ssid]
		if s == nil {
			s = &seen{n: Network{SSID: ssid, Signal: -1}}
			best[ssid] = s
		}
		s.n.Signal = max(s.n.Signal, int32(min(max(signal, 0), 100)))
		s.n.Current = s.n.Current || unescape(f[0]) == "yes"
		s.anySecured = s.anySecured || secured
		if !unsupportedSecurity(security) {
			s.joinable = true
			s.joinSecured = s.joinSecured || secured
		}
	}
	networks := make([]Network, 0, len(best))
	for _, s := range best {
		n := s.n
		n.Unsupported = !s.joinable
		n.Secured = s.joinSecured || (!s.joinable && s.anySecured)
		networks = append(networks, n)
	}
	sort.Slice(networks, func(i, j int) bool {
		a, b := networks[i], networks[j]
		if a.Current != b.Current {
			return a.Current
		}
		if a.Signal != b.Signal {
			return a.Signal > b.Signal
		}
		return a.SSID < b.SSID
	})
	return networks
}

// decodeSSID turns nmcli's SSID-HEX into the name: false for a hidden
// network (no name) and for a name that isn't valid UTF-8.
func decodeSSID(h string) (string, bool) {
	h = strings.TrimSpace(h)
	if h == "" || h == "--" {
		return "", false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || len(raw) == 0 || len(raw) > cMaxSSIDBytes || !utf8.Valid(raw) {
		return "", false
	}
	return string(raw), true
}

// unsupportedSecurity: enterprise (802.1X), WEP and Enhanced Open (OWE)
// networks, which the runner doesn't join.
func unsupportedSecurity(security string) bool {
	for _, w := range strings.Fields(cSecurityWords) {
		if strings.Contains(security, w) {
			return true
		}
	}
	return false
}

// splitUnescaped splits one line of nmcli's terse output, where a ':'
// inside a field is written '\:' (and a '\' as '\\').
func splitUnescaped(s string, sep byte) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == sep {
			fields = append(fields, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	fields = append(fields, cur.String())
	return fields
}

func unescape(s string) string {
	return strings.NewReplacer(`\:`, ":", `\\`, `\`).Replace(s)
}

// LogVersion logs NetworkManager's version once, at start (the main
// instance, not in a container): Settings > Logs then shows which one a
// device runs, which the Wi-Fi change depends on.
func LogVersion() {
	ctx, cancel := context.WithTimeout(context.Background(), cQueryTimeout)
	defer cancel()
	out, err := nmcli(ctx, "--version")
	if err != nil {
		log.Info("network: nmcli not available (Settings > Wi-Fi is off):", err)
		return
	}
	log.Info("network:", string(bytes.TrimSpace(out)))
}
