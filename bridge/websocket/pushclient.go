// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
)

// bridgeWebPushClient sends the offline alert's Web Push. The endpoints
// are whatever a device stored, so it only dials public addresses - checked
// after DNS resolution, so a name that points (or rebinds) into the
// WireGuard network or at loopback is refused too - and follows no
// redirect (push services don't redirect; a 3xx is logged, not followed).
var bridgeWebPushClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: denyNonPublic}).DialContext
	return &http.Client{
		Timeout:       15 * time.Second,
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}()

var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT, Tailscale
}

// denyNonPublic is a net.Dialer Control refusing every address but a
// public unicast one.
func denyNonPublic(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	refused := ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
	for _, p := range notPublic {
		refused = refused || p.Contains(ip)
	}
	if refused {
		return fmt.Errorf("web push to %s refused: not a public address", ip)
	}
	return nil
}

// cMaxPushRegistrations caps each kind of registration a device stores
// here; a device has a handful.
const cMaxPushRegistrations = 100

// webPushEndpointOK: an https URL that fits push_web_subs.endpoint.
func webPushEndpointOK(endpoint string) bool {
	if len(endpoint) > 1000 {
		return false
	}
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// filterPushRegistrations keeps what a device sent that the bridge can
// store and use - an https Web Push endpoint, tokens that fit their
// columns, no duplicates, cMaxPushRegistrations of each kind - and
// reports how many it left out. Dropping, not refusing: an older device
// with one odd entry still gets its offline alerts.
func filterPushRegistrations(req *pb.UpdatePushRegistrations) (apns, fcm []string, web []push.WebPushSubscription, dropped int) {
	tokens := func(in []string, maxLen int) []string {
		seen := map[string]bool{}
		var out []string
		for _, t := range in {
			if t == "" || len(t) > maxLen || seen[t] || len(out) == cMaxPushRegistrations {
				dropped++
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
		return out
	}
	apns = tokens(req.ApnsTokens, 255) // push_apns_tokens.token
	fcm = tokens(req.FcmTokens, 512)   // push_fcm_tokens.token
	seen := map[string]bool{}
	for _, s := range req.WebPushSubs {
		if !webPushEndpointOK(s.Endpoint) || len(s.P256Dh) > 255 || len(s.Auth) > 255 || seen[s.Endpoint] || len(web) == cMaxPushRegistrations {
			dropped++
			continue
		}
		seen[s.Endpoint] = true
		web = append(web, push.WebPushSubscription{Endpoint: s.Endpoint, P256dh: s.P256Dh, Auth: s.Auth})
	}
	return apns, fcm, web, dropped
}
