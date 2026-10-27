// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/lantls"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/supervisor"
)

// The device in its container image (docker/, container.Enabled): what the
// server it runs on looks after instead of the device, and who may choose
// its first owner password. None of this runs without OTC_CONTAINER.
//
// A Raspberry Pi takes its first password from whoever opens it first,
// which is fine for a box on the owner's own network that nobody has
// heard of yet. A container's port can be anywhere - on a server with a
// public address, behind the owner's reverse proxy - so the primary
// instance takes its first password only from the home network, straight
// to its own port from its own page, and only in the 30 minutes after its
// first start (the start-up script's OTC_SETUP_UNTIL). Past that the owner
// sets it on the server (otc-entrypoint set-password). An additional
// user's instance, whose ports are the server's too, takes its first
// password the same way but at any time, or through the bridge.

// containerGate answers, in a container, a request the device must not
// run as usual (handled true); the rest go on to the usual switches.
// Beside demoGate in processMessage.
func (ch *connHandler) containerGate(env *pb.ReqEnvelope, lang string) (resp *pb.RespEnvelope, handled bool) {
	if _, ok := env.Payload.(*pb.ReqEnvelope_ReqAuth); ok {
		if refusal, refused := ch.firstPasswordRefusal(time.Now()); refused {
			return &pb.RespEnvelope{
				Id: env.Id,
				// No Code, as for a password too short: the phone apps
				// take any sign-in code for the bridge's "device
				// unreachable".
				Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: false, ErrorMsg: refusal.Render(lang)}},
			}, true
		}
		return nil, false
	}

	// The machine's disks and network are the server's. Answered to a
	// signed-in owner, as the usual handlers are; without a session the
	// usual refusal stands.
	if ch.getSession() == nil {
		return nil, false
	}
	switch p := env.Payload.(type) {
	case *pb.ReqEnvelope_ReqListStorageDevices:
		// The server's disks are no choice of the device's: none listed.
		return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespStorageDevices{RespStorageDevices: &pb.StorageDevices{}}}, true
	case *pb.ReqEnvelope_ReqListWifiNetworks:
		return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespWifiNetworks{RespWifiNetworks: &pb.WifiNetworks{}}}, true
	case *pb.ReqEnvelope_ReqSetupStorage:
		// No disks chosen is what a first-run setup sends when it skips
		// the step: nothing to do. Any disk is refused.
		if len(p.ReqSetupStorage.DevicePaths) == 0 {
			return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}, true
		}
		return notInContainer(env.Id, lang), true
	case *pb.ReqEnvelope_ReqGetWifiStatus:
		// Settings > Wi-Fi is hidden: the server's network.
		return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespWifiStatus{RespWifiStatus: &pb.WifiStatus{}}}, true
	case *pb.ReqEnvelope_ReqJoinWifi:
		return notInContainer(env.Id, lang), true
	case *pb.ReqEnvelope_ReqUseMirrorDisk:
		// otc-mirror adds what .env names; Status offers no disk here.
		return notInContainer(env.Id, lang), true
	}
	return nil, false
}

// notInContainer refuses what the server the container runs on looks after.
func notInContainer(id int32, lang string) *pb.RespEnvelope {
	return &pb.RespEnvelope{Id: id, Error: true, ErrorMessage: i18n.DevErrNotInContainer().Render(lang)}
}

// firstPasswordRefusal is why this connection may not choose the first
// password of this instance at now (refused true), or refused false: the
// password is already set (an ordinary sign-in), or the rules below allow
// it.
//
// While the primary has no password, a sign-in is refused when it comes
// through the bridge, when the connection carried a proxy's headers or
// came from another site's page (ch.proxied), when its own peer is not a
// home address (container.HomeAddress), or once OTC_SETUP_UNTIL has passed
// (or is missing). The peer is the connection's own, never
// X-Forwarded-For nor the bridge's BridgeClientInfo, which the
// password-attempt limit uses: a home address in a header proves nothing.
//
// An additional user's instance (supervisor.ChildEnvVar - its own
// environment, so a primary that couldn't start its supervisor is still
// the primary) takes its first password through the bridge, as it usually
// comes, or from the home network as above, at any time: the owner adds
// users whenever they like, and the user's own address on the bridge is
// what reaches it from anywhere else.
//
// Whether a password is set must be known: a sign-in that went on
// without knowing could choose the first one unchecked.
func (ch *connHandler) firstPasswordRefusal(now time.Time) (i18n.Msg, bool) {
	defined, err := ch.mg.dao.IsSecretDefined()
	if err != nil {
		log.Error("first password: could not tell whether one is set:", err)
		return i18n.DevAuthDeviceStateFailed(), true
	}
	if defined {
		return i18n.Msg{}, false
	}
	home := !ch.fromBridge && !ch.proxied && container.HomeAddress(ch.peer)
	if os.Getenv(supervisor.ChildEnvVar) != "" {
		if ch.fromBridge || home {
			return i18n.Msg{}, false
		}
		log.Info("first password refused, not from the home network nor through the bridge: peer", ch.peer, "proxied", ch.proxied)
		return i18n.DevAuthFirstPasswordUserHomeOnly(), true
	}
	switch {
	case !home:
		log.Info("first owner password refused, not from the home network: peer", ch.peer, "proxied", ch.proxied, "through the bridge", ch.fromBridge)
		return i18n.DevAuthFirstPasswordHomeOnly(), true
	case !container.SetupWindowOpen(now):
		log.Info("first owner password refused: its 30 minutes are over (set it with otc-entrypoint set-password)")
		return i18n.DevAuthFirstPasswordWindowClosed(), true
	}
	return i18n.Msg{}, false
}

// peerHost is the connection's own peer address, without its port.
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// indirect reports whether r did not come straight from the device's own
// page or an app: through a proxy (proxyHeaders), or from a page that
// isn't this listener's own (foreignOrigin).
func indirect(r *http.Request) bool {
	return proxyHeaders(r) || foreignOrigin(r)
}

// proxyHeaders reports whether r carried any of the headers a proxy, or a
// CDN in front of one, adds for the client behind it.
func proxyHeaders(r *http.Request) bool {
	for _, h := range []string{
		"X-Forwarded-For", "Forwarded", "X-Real-IP", "X-Forwarded-Host", "X-Forwarded-Proto",
		"Via", "CF-Connecting-IP", "True-Client-IP",
	} {
		if len(r.Header.Values(h)) > 0 {
			return true
		}
	}
	return false
}

// foreignOrigin reports whether a browser opened r from a page that isn't
// this listener's own: another site's - which any page open on the home
// network could be - or the device's page behind a TLS proxy that adds no
// forwarding headers (https in front of the plain port, or another host).
// The apps send no Origin; a browser always does.
func foreignOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || o == "null" {
		return true
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	want := "http"
	if r.TLS != nil {
		want = "https"
	}
	return u.Scheme != want || !strings.EqualFold(u.Host, r.Host)
}

// EnvTrustedProxies is the container's setting for [otc-api]
// trusted-proxies.
const EnvTrustedProxies = "OTC_TRUSTED_PROXIES"

// trustedProxies are the peers whose X-Forwarded-For a container
// believes: [otc-api] trusted-proxies, else EnvTrustedProxies - which the
// start-up script writes the key from, and which is all an additional
// user's instance has (its config doesn't carry the key). Read once.
var trustedProxies = sync.OnceValue(func() []netip.Prefix {
	v := ""
	if cfg.HasSection("otc-api") {
		v = cfg.GetStr("otc-api", "trusted-proxies")
	}
	if strings.TrimSpace(v) == "" {
		v = os.Getenv(EnvTrustedProxies)
	}
	return parseTrustedProxies(v)
})

// parseTrustedProxies reads a list of addresses and ranges (CIDR),
// separated by commas or spaces; an entry that is neither is logged and
// left out.
func parseTrustedProxies(v string) []netip.Prefix {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				log.Error("trusted-proxies: not an address range, left out:", f)
				continue
			}
			if p.Addr().Is4In6() {
				p = netip.PrefixFrom(p.Addr().Unmap(), max(p.Bits()-96, 0))
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			log.Error("trusted-proxies: not an address, left out:", f)
			continue
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out
}

// forwardedClient is the address a container limits password attempts
// by, for a connection from peer that carried xff (every X-Forwarded-For
// line): peer itself unless it is a trusted proxy; then the nearest hop in
// the header that isn't one, read from the right - the left of the header
// is whatever the client chose to send. A hop that isn't an address ends
// the reading at peer.
func forwardedClient(peer string, xff []string, trusted []netip.Prefix) string {
	p, err := netip.ParseAddr(peer)
	if err != nil || !inPrefixes(p.Unmap(), trusted) {
		return peer
	}
	var hops []string
	for _, line := range xff {
		for _, h := range strings.Split(line, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := hopAddr(hops[i])
		if !ok {
			return peer
		}
		if i == 0 || !inPrefixes(a, trusted) {
			return a.String()
		}
	}
	return peer
}

// hopAddr is one X-Forwarded-For entry's address: "a.b.c.d", "a:b::c",
// or either with a port.
func hopAddr(h string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(h); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// portFree reports whether port can be listened on here - with host
// networking, on the server itself. A var for tests.
var portFree = func(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// primaryPorts are the ports the primary instance and its database hold:
// its HTTP and home-network TLS ports and MariaDB's.
func primaryPorts() []int {
	var out []int
	if cfg.HasSection("otc-api") {
		httpPort := int(cfg.GetInt("otc-api", "port"))
		out = append(out, httpPort)
		if tls, err := lantls.Port(cfg.GetStr("otc-api", "lan-tls-port"), httpPort); err == nil {
			out = append(out, tls)
		}
	}
	if cfg.HasSection("mysql") {
		out = append(out, int(cfg.GetInt("mysql", "port")))
	}
	return out
}

// containerUserPort is the HTTP port for a new user's instance in a
// container: requested when it is usable, else - for 0 - the next usable
// one after the primary's HTTP port. The ports are the server's (host
// networking), so a port is usable only when it and the instance's TLS
// port beside it (lantls.Port) are free on the server, are none of the
// primary's, and are no other user's.
func (ch *connHandler) containerUserPort(requested int) (int, error) {
	reserved := primaryPorts()
	usable := func(p int) (bool, error) {
		return userPortUsable(p, reserved, ch.mg.dao.IsPortInUse, portFree)
	}
	if requested != 0 {
		ok, err := usable(requested)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, devtext.New(i18n.DevUsersPortInUse(strconv.Itoa(requested)))
		}
		return requested, nil
	}
	httpPort := 8080
	if len(reserved) > 0 && reserved[0] > 0 {
		httpPort = reserved[0]
	}
	start, err := ch.mg.dao.NextFreePort(httpPort + 1)
	if err != nil {
		return 0, err
	}
	return nextUserPort(start, usable)
}

// cUserPortTries bounds the search for a free port.
const cUserPortTries = 1000

// nextUserPort is the first port from start on that usable accepts.
func nextUserPort(start int, usable func(int) (bool, error)) (int, error) {
	for p := start; p < start+cUserPortTries && p <= 65535; p++ {
		ok, err := usable(p)
		if err != nil {
			return 0, err
		}
		if ok {
			return p, nil
		}
	}
	return 0, devtext.New(i18n.DevUsersPortInUse(strconv.Itoa(start)))
}

// userPortUsable: p and its TLS port beside it are valid ports, none of
// reserved, held by no user (inUse: a user's HTTP port; a user's own TLS
// port is its HTTP port plus the same offset), and free (portFree).
func userPortUsable(p int, reserved []int, inUse func(int) (bool, error), free func(int) bool) (bool, error) {
	if p <= 0 {
		return false, nil
	}
	tls, err := lantls.Port("", p)
	if err != nil {
		return false, nil
	}
	for _, r := range reserved {
		if p == r || tls == r {
			return false, nil
		}
	}
	for _, q := range []int{p, tls, p - (tls - p)} {
		if q <= 0 {
			continue
		}
		taken, err := inUse(q)
		if err != nil {
			return false, err
		}
		if taken {
			return false, nil
		}
	}
	return free(p) && free(tls), nil
}

// containerUpdateInfo marks an update answer as a container's: it is
// updated by pulling its image, which message says (unless it already
// says how the last update went).
func containerUpdateInfo(out *pb.RespUpdateInfo, lang string) {
	out.Container = true
	if out.Message == "" {
		out.Message = i18n.DevUpdateContainerPull().Render(lang)
	}
}
