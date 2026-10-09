// SPDX-License-Identifier: AGPL-3.0-or-later

package wifiwatch

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procRoot and sysRoot are vars so tests can point them at a fake tree.
var (
	procRoot = "/proc"
	sysRoot  = "/sys"
)

// sample is one look at the counters. Every counter only ever grows,
// except that an interface's own counters start again from zero when its
// driver is reloaded - which a window then skips (see newWindow).
type sample struct {
	at time.Time
	// iface is the wireless interface the default route goes through, ""
	// when the default route isn't wireless (Ethernet) or there is none.
	iface string

	// TCP over every interface, from /proc/net/snmp.
	outSegs, retransSegs uint64
	// loopback's packets, taken off outSegs: the device talks to itself
	// over TCP (MariaDB, its own media stream), never retransmits there,
	// and would otherwise water the Wi-Fi's ratio down.
	loTxPackets uint64

	// The wireless interface's own counters (sysfs).
	txBytes, txErrors, txDropped uint64

	// From `iw dev <iface> station dump` (the access point): frames the
	// radio sent and those never acknowledged. staOK is false when iw
	// isn't there or didn't answer.
	staTxPackets, staTxFailed uint64
	staOK                     bool
}

// readSample reads every counter. Only a failure to read /proc/net/snmp
// is an error; the rest is best effort.
func readSample(now time.Time) (sample, error) {
	s := sample{at: now}
	out, retrans, err := readTCP()
	if err != nil {
		return s, err
	}
	s.outSegs, s.retransSegs = out, retrans
	s.loTxPackets = readCounter("lo", "tx_packets")
	s.iface = defaultWirelessIface()
	if s.iface == "" {
		return s, nil
	}
	s.txBytes = readCounter(s.iface, "tx_bytes")
	s.txErrors = readCounter(s.iface, "tx_errors")
	s.txDropped = readCounter(s.iface, "tx_dropped")
	s.staTxPackets, s.staTxFailed, s.staOK = stationCounters(s.iface)
	return s, nil
}

// readTCP returns Tcp OutSegs and RetransSegs from /proc/net/snmp, which
// holds a header line and a value line per protocol.
func readTCP() (outSegs, retransSegs uint64, err error) {
	raw, err := os.ReadFile(filepath.Join(procRoot, "net/snmp"))
	if err != nil {
		return 0, 0, err
	}
	return parseSNMP(raw)
}

func parseSNMP(raw []byte) (outSegs, retransSegs uint64, err error) {
	var header []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != "Tcp:" {
			continue
		}
		if header == nil {
			header = fields
			continue
		}
		var gotOut, gotRetrans bool
		for i := 1; i < len(fields) && i < len(header); i++ {
			v, perr := strconv.ParseUint(fields[i], 10, 64)
			if perr != nil {
				continue
			}
			switch header[i] {
			case "OutSegs":
				outSegs, gotOut = v, true
			case "RetransSegs":
				retransSegs, gotRetrans = v, true
			}
		}
		if !gotOut || !gotRetrans {
			return 0, 0, fmt.Errorf("no Tcp OutSegs/RetransSegs in /proc/net/snmp")
		}
		return outSegs, retransSegs, nil
	}
	return 0, 0, fmt.Errorf("no Tcp lines in /proc/net/snmp")
}

// defaultWirelessIface is the interface of the IPv4 default route with
// the lowest metric, when it is a wireless one.
func defaultWirelessIface() string {
	raw, err := os.ReadFile(filepath.Join(procRoot, "net/route"))
	if err != nil {
		return ""
	}
	iface := defaultRouteIface(raw)
	if iface == "" || !isWireless(iface) {
		return ""
	}
	return iface
}

// defaultRouteIface parses /proc/net/route: Iface Destination Gateway
// Flags RefCnt Use Metric Mask ... in hex, the default route being
// destination and mask 0, up (flag 0x1).
func defaultRouteIface(raw []byte) string {
	best, bestMetric := "", uint64(0)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[0] == "Iface" || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&1 == 0 {
			continue
		}
		metric, err := strconv.ParseUint(f[6], 10, 64)
		if err != nil {
			continue
		}
		if best == "" || metric < bestMetric {
			best, bestMetric = f[0], metric
		}
	}
	return best
}

func isWireless(iface string) bool {
	if iface == "." || iface == ".." || strings.Contains(iface, "/") {
		return false
	}
	for _, p := range []string{"wireless", "phy80211"} {
		if _, err := os.Stat(filepath.Join(sysRoot, "class/net", iface, p)); err == nil {
			return true
		}
	}
	return false
}

// readCounter reads /sys/class/net/<iface>/statistics/<name>, 0 when it
// can't.
func readCounter(iface, name string) uint64 {
	raw, err := os.ReadFile(filepath.Join(sysRoot, "class/net", iface, "statistics", name))
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return v
}

// stationCounters runs `iw dev <iface> station dump`, which works
// unprivileged, and adds up what it says for every station (a client has
// one: its access point).
var stationCounters = func(iface string) (txPackets, txFailed uint64, ok bool) {
	iw := findIw()
	if iw == "" {
		return 0, 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, iw, "dev", iface, "station", "dump").Output()
	if err != nil {
		return 0, 0, false
	}
	return parseStationDump(out)
}

func findIw() string {
	if p, err := exec.LookPath("iw"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/sbin/iw", "/sbin/iw"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func parseStationDump(out []byte) (txPackets, txFailed uint64, ok bool) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	var seenPackets bool
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(name) {
		case "tx packets":
			txPackets += v
			seenPackets = true
		case "tx failed":
			txFailed += v
		}
	}
	return txPackets, txFailed, seenPackets
}
