// Package network implements the WiFi-scan side of first-boot network
// setup (issue #38) — reaching a device over its own temporary "Off The
// Cloud" access point rather than an already-working connection.
//
// ListNetworks is safe and read-only (nmcli's scan results don't need any
// special privilege), so it runs directly inside the otc service's own
// unprivileged process, same as storage.ListDevices.
//
// Actually joining a network needs root (nmcli connection up/add), which
// that process can't do on its own — see RequestJoin, which hands the
// request off to scripts/network_setup.py the same way storage.RequestSetup
// hands RAID setup off to scripts/raid_watch.py.
package network

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/log"
)

type Network struct {
	SSID    string
	Signal  int32
	Secured bool
}

// ListNetworks runs `nmcli dev wifi list` and parses its terse (-t) output.
// Duplicate SSIDs (the same network seen on multiple channels/APs) are
// collapsed to the strongest signal seen, since the setup UI just needs
// "networks a person could pick", not raw AP-level detail.
func ListNetworks() ([]Network, error) {
	out, err := exec.Command("nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY", "dev", "wifi", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("nmcli dev wifi list: %w", err)
	}

	best := map[string]Network{}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		// nmcli -t escapes literal ':' inside fields as '\:', which
		// naive strings.Split(line, ":") would otherwise misparse.
		fields := splitUnescaped(line, ':')
		if len(fields) < 3 {
			continue
		}
		ssid := unescape(fields[0])
		if ssid == "" {
			// Hidden networks report an empty SSID — nothing a
			// person could usefully pick from a list.
			continue
		}
		signal, _ := strconv.ParseInt(fields[1], 10, 32)
		secured := unescape(fields[2]) != "" && unescape(fields[2]) != "--"

		if existing, ok := best[ssid]; !ok || int32(signal) > existing.Signal {
			best[ssid] = Network{SSID: ssid, Signal: int32(signal), Secured: secured}
		}
	}

	networks := make([]Network, 0, len(best))
	for _, n := range best {
		networks = append(networks, n)
	}
	return networks, nil
}

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

// RequestFile is where RequestJoin writes the requested network, for
// scripts/network_setup.py (root) to pick up and actually join.
const RequestFile = "/var/lib/otc/wifi_join_request.json"

type joinRequest struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// RequestJoin hands a join request off to network_setup.py — see the
// package comment for why this service can't do it itself.
func RequestJoin(ssid, password string) error {
	if ssid == "" {
		return fmt.Errorf("ssid cannot be empty")
	}

	data, err := json.Marshal(joinRequest{SSID: ssid, Password: password})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(RequestFile), 0755); err != nil {
		return err
	}

	log.Info("network: writing wifi join request for ssid:", ssid)
	return os.WriteFile(RequestFile, data, 0644)
}
