// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"encoding/hex"
	"log"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	"github.com/alonsovidales/otc/i18n"
)

// Issue #190: the device on the home network. The client tries it first
// on every dial and learns it after each sign-in elsewhere (wsclient);
// the engine keeps what it learnt, says which way it is connected, and
// chooses the route again when the computer changes networks.

// networkCheckInterval is how often the interfaces are looked at: there
// is no portable change notification, and reading them is cheap.
const networkCheckInterval = 5 * time.Second

// LocalEndpointFor is the home-network endpoint kept for domain, for a
// client; nil when there is none.
func LocalEndpointFor(domain string) *wsclient.LocalEndpoint {
	st := config.LoadLocalEndpoint(domain)
	if st == nil {
		return nil
	}
	pin, err := hex.DecodeString(st.CertSHA256)
	if err != nil {
		return nil
	}

	return wsclient.NewLocalEndpoint(st.Addresses, st.Port, pin)
}

// keepLocalEndpoint stores, or with nil forgets, what the device at domain
// said about its home-network endpoint. Under e.mu, so an answer from the
// device before a change of device is never stored after it.
func (e *Engine) keepLocalEndpoint(domain string, ep *wsclient.LocalEndpoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped || e.cfg == nil || e.cfg.Domain != domain {
		return
	}
	if ep == nil {
		via := viaName(domain)
		if via == "" {
			via = "the configured address" // i18n-ignore: log text
		}
		log.Printf("the device can't be reached on the home network: connecting through %s", via)
		if err := config.ClearLocalEndpoint(); err != nil {
			log.Printf("could not forget the home-network endpoint: %v", err)
		}

		return
	}
	log.Printf("the device answers on the home network at %s, port %d", strings.Join(ep.Addresses, ", "), ep.Port)
	err := config.SaveLocalEndpoint(&config.LocalEndpoint{
		Domain: domain, Addresses: ep.Addresses, Port: ep.Port, CertSHA256: hex.EncodeToString(ep.Pin),
	})
	if err != nil {
		log.Printf("could not keep the home-network endpoint: %v", err)
	}
}

// RouteLine is a connected client's status line: which way it reaches
// the device, in the words every app uses - in English, for the command
// line.
func RouteLine(route, domain string) string { return RouteMsg(route, domain).Render("") }

// RouteMsg is RouteLine as a catalog message, for the tray.
func RouteMsg(route, domain string) i18n.Msg {
	switch wsclient.Route(route) {
	case wsclient.RouteLocal:
		return i18n.DeskMainConnectedHome()
	case wsclient.RouteRemote:
		if via := viaName(domain); via != "" {
			return i18n.DeskMainConnectedThrough(via)
		}

		return i18n.DeskMainConnectedThroughConfigured()
	}

	return i18n.DeskMainStatusConnected()
}

// StatusLine is the status, with the route once connected, in English: the
// command line.
func StatusLine(st config.State, domain string) string { return StatusText("", st, domain) }

// StatusText is the status line in lang (the tray's): the status's code
// worded, with the route once connected. A code this build doesn't know -
// a newer service's - shows as it is.
func StatusText(lang string, st config.State, domain string) string {
	if st.Status == StatusConnected && st.Route != "" {
		return RouteMsg(st.Route, domain).Render(lang)
	}
	if m, ok := statusMsg(st.Status); ok {
		return m.Render(lang)
	}

	return st.Status
}

// statusMsg is a status code's words.
func statusMsg(status string) (i18n.Msg, bool) {
	switch status {
	case StatusNotConnected:
		return i18n.DeskMainStatusNotConnected(), true
	case StatusConnecting:
		return i18n.CommonConnecting(), true
	case StatusConnected:
		return i18n.DeskMainStatusConnected(), true
	case StatusDisconnected:
		return i18n.DeskMainStatusDisconnected(), true
	case StatusOffline:
		return i18n.DeskMainStatusOffline(), true
	case StatusWrongPassword:
		return i18n.DeskMainStatusWrongPassword(), true
	case StatusMissingSettings:
		return i18n.DeskMainStatusMissingSettings(), true
	case StatusWaitingPassword:
		return i18n.DeskMainStatusWaitingPassword(), true
	case StatusSyncNotRunning:
		return i18n.DeskMainStatusSyncNotRunning(), true
	}
	if s, ok := TooManyAttempts(status); ok {
		return i18n.DeskMainStatusTooManyAttempts(s), true
	}

	return i18n.Msg{}, false
}

// viaName is "off-the.cloud" for a device on the bridge, else the host of
// the custom address; "" when it has none.
func viaName(domain string) string {
	if config.BridgeName(domain) != "" {
		return config.BridgeDomain
	}
	host := strings.TrimSpace(domain)
	if strings.Contains(host, "://") {
		if u, err := url.Parse(host); err == nil {
			host = u.Hostname()
		}
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if h := strings.ToLower(host); h == config.BridgeDomain || strings.HasSuffix(h, "."+config.BridgeDomain) {
		return config.BridgeDomain
	}
	return host
}

// netIface is what the network check reads of an interface.
type netIface struct {
	Name     string
	Up       bool
	Loopback bool
	Addrs    []net.IP
}

// listInterfaces reads the interfaces (replaced in the tests).
var listInterfaces = func() ([]netIface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]netIface, 0, len(ifs))
	for _, i := range ifs {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		ni := netIface{Name: i.Name, Up: i.Flags&net.FlagUp != 0, Loopback: i.Flags&net.FlagLoopback != 0}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				ni.Addrs = append(ni.Addrs, n.IP)
			}
		}
		out = append(out, ni)
	}

	return out, nil
}

// ignoredIfacePrefixes are containers' and VPNs' interfaces: they come and
// go without changing the way to the device.
var ignoredIfacePrefixes = []string{"docker", "veth", "br-", "virbr", "tailscale", "utun"}

// networkFingerprint names the networks this computer is on: the IPv4
// addresses and IPv6 /64 prefixes of the interfaces that are up, without
// loopback, link-local or ignored interfaces. IPv6 counts by prefix, so a
// rotating privacy address is not a change.
func networkFingerprint(ifs []netIface) string {
	var keys []string
	for _, i := range ifs {
		if !i.Up || i.Loopback || ignoredIface(i.Name) {
			continue
		}
		for _, ip := range i.Addrs {
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				keys = append(keys, i.Name+" "+v4.String())

				continue
			}
			keys = append(keys, i.Name+" "+ip.Mask(net.CIDRMask(64, 128)).String()+"/64")
		}
	}
	sort.Strings(keys)

	return strings.Join(keys, ",")
}

func ignoredIface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range ignoredIfacePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}

	return false
}

// watchNetwork calls networkChanged whenever the networks this computer
// is on change, until stop is closed.
func (e *Engine) watchNetwork(stop <-chan struct{}) {
	last := ""
	if ifs, err := listInterfaces(); err == nil {
		last = networkFingerprint(ifs)
	}
	t := time.NewTicker(networkCheckInterval)
	defer t.Stop()
	prev := time.Now()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		now := time.Now()
		if slept(now.Sub(prev), now.Round(0).Sub(prev.Round(0))) {
			e.woke()
		}
		prev = now
		ifs, err := listInterfaces()
		if err != nil {
			continue
		}
		if fp := networkFingerprint(ifs); fp != last {
			last = fp
			e.networkChanged()
		}
	}
}

// wakeGap: a network check that comes this much later than due means the
// computer slept.
const wakeGap = 20 * time.Second

// slept says whether the time between two network checks, by the
// monotonic clock and by the wall clock, is a sleep: Linux's monotonic
// clock stops while the computer sleeps and the wall clock doesn't;
// Windows' moves on with it.
func slept(mono, wall time.Duration) bool {
	due := networkCheckInterval + wakeGap

	return mono > due || wall > due
}

// woke is the Mac's wake (SyncModel.scheduleRouteCheck(wake: true)): the
// computer may be home again, or away. Nothing while the connection is
// the home one - it is fine, or its pings find it dead and it dials again
// (wsclient's keep-alive) -; otherwise the route is chosen again, as for a
// network change.
func (e *Engine) woke() {
	log.Printf("the computer woke up")
	if e.ws.Route() == wsclient.RouteLocal {
		return
	}
	e.networkChanged()
}

// networkChanged chooses the route again - the computer may have come
// home, or left - unless something is being transferred (a folder pass
// or the change queue, as syncInFlight on macOS): that finishes where it
// is, and the next reconnect chooses. A request alone, such as the RAID
// poll, is no transfer: one left unanswered on a connection that died
// unnoticed would keep the client there until TCP gives up. Without a
// home-network endpoint (a device before issue #190) there is no other
// route, and nothing changes.
func (e *Engine) networkChanged() {
	e.mu.Lock()
	ready := !e.stopped && config.Ready(e.cfg, e.password)
	busy := len(e.folderBusy) > 0 || len(e.draining) > 0
	e.mu.Unlock()
	if !ready || !e.ws.HasLocalEndpoint() {
		return
	}
	if busy {
		log.Printf("the network changed during a transfer: the route is chosen again at the next reconnect")

		return
	}
	if e.ws.Reconnect() {
		log.Printf("the network changed: connecting again, the home network first")
	}
}
