// SPDX-License-Identifier: AGPL-3.0-or-later

package lantls

import (
	"net"
	"strings"
)

// cHotspotIface is the setup hotspot's interface (scripts/network_setup.py):
// a phone on it is next to the device, not on the home network.
const cHotspotIface = "uap0"

// tailnetV6 is Tailscale's IPv6 range, inside the ULA block; its IPv4 one
// (100.64.0.0/10) isn't private and never passes.
var tailnetV6 = mustCIDR("fd7a:115c:a1e0::/48")

// iface is what Addresses reads of a network interface.
type iface struct {
	Name  string
	Flags net.Flags
	Addrs []net.Addr
}

// interfaces lists this machine's interfaces; replaced in tests.
var interfaces = func() ([]iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]iface, 0, len(ifs))
	for _, i := range ifs {
		addrs, err := i.Addrs()
		if err != nil {
			// One unreadable interface doesn't hide the others.
			continue
		}
		out = append(out, iface{Name: i.Name, Flags: i.Flags, Addrs: addrs})
	}
	return out, nil
}

// Addresses are where an app at home can reach this device: the private
// IPv4 (10/8, 172.16/12, 192.168/16) and IPv6 ULA (fc00::/7) addresses of
// its up interfaces, IPv4 first. Left out: loopback and link-local, the
// setup hotspot and anything in its subnet, Tailscale (reached through the
// tailnet, not the home network) and containers.
func Addresses() ([]string, error) {
	ifs, err := interfaces()
	if err != nil {
		return nil, err
	}
	return filterAddresses(ifs), nil
}

func filterAddresses(ifs []iface) []string {
	var hotspot []*net.IPNet
	for _, i := range ifs {
		if i.Name != cHotspotIface {
			continue
		}
		for _, a := range i.Addrs {
			if n, ok := a.(*net.IPNet); ok {
				hotspot = append(hotspot, n)
			}
		}
	}

	seen := map[string]bool{}
	var v4, v6 []string
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || skippedIface(i.Name) {
			continue
		}
		for _, a := range i.Addrs {
			ip := ipOf(a)
			if ip == nil || !ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || tailnetV6.Contains(ip) || inAny(hotspot, ip) {
				continue
			}
			s := ip.String()
			if seen[s] {
				continue
			}
			seen[s] = true
			if ip.To4() != nil {
				v4 = append(v4, s)
			} else {
				v6 = append(v6, s)
			}
		}
	}
	return append(v4, v6...)
}

func skippedIface(name string) bool {
	if name == cHotspotIface {
		return true
	}
	for _, p := range []string{"tailscale", "docker", "br-", "veth"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func ipOf(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	}
	return nil
}

func inAny(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}
