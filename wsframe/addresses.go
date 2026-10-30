// SPDX-License-Identifier: AGPL-3.0-or-later

package wsframe

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// A name that doesn't resolve this time is dialled at the addresses it last
// resolved to. On 2026-10-30 a device's update saturated its home link and
// the router's DNS stopped answering it: the bridge's names live a minute
// (TTL 60), every connection of the pool dials anew, so the device spent
// twenty minutes off the bridge while it ran fine at home. TLS still checks
// the certificate against the name (the dialer only supplies the TCP
// connection), so an old or wrong address can fail but never impersonate.
const (
	// LookupTimeout bounds one name lookup, so a resolver that doesn't
	// answer leaves DialTimeout room for the remembered addresses.
	LookupTimeout = 5 * time.Second
	// cMaxNames bounds the remembered names (the bridge's, friends'
	// devices behind it).
	cMaxNames = 256
)

var (
	resolver = net.DefaultResolver
	netDial  = (&net.Dialer{}).DialContext

	known = struct {
		sync.Mutex
		addrs map[string][]string
		file  string
	}{addrs: map[string][]string{}}
)

// KeepAddressesIn remembers the addresses across restarts in path (loaded
// now, rewritten when they change): a device restarted while its resolver
// is down still finds the bridge. Unreadable or missing, it starts empty.
func KeepAddressesIn(path string) {
	known.Lock()
	defer known.Unlock()
	known.file = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved map[string][]string
	if json.Unmarshal(data, &saved) != nil {
		return
	}
	for name, ips := range saved {
		if len(known.addrs) >= cMaxNames {
			break
		}
		if valid := validIPs(ips); len(valid) > 0 {
			known.addrs[name] = valid
		}
	}
}

func validIPs(ips []string) []string {
	var out []string
	for _, ip := range ips {
		if net.ParseIP(ip) != nil {
			out = append(out, ip)
		}
	}
	return out
}

// remember records name's addresses, and writes them out when they changed.
func remember(name string, ips []string) {
	sorted := append([]string(nil), ips...)
	sort.Strings(sorted)
	known.Lock()
	defer known.Unlock()
	if prev, ok := known.addrs[name]; ok && equal(prev, sorted) {
		return
	}
	if _, ok := known.addrs[name]; !ok && len(known.addrs) >= cMaxNames {
		return
	}
	known.addrs[name] = sorted
	if known.file == "" {
		return
	}
	data, err := json.Marshal(known.addrs)
	if err != nil {
		return
	}
	// Beside it, then renamed: a reader (another instance starting) never
	// sees half a file. A failure only means none are kept across restarts.
	tmp, err := os.CreateTemp(filepath.Dir(known.file), ".addresses-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr != nil || cerr != nil || os.Rename(tmp.Name(), known.file) != nil {
		os.Remove(tmp.Name())
	}
}

func lastKnown(name string) []string {
	known.Lock()
	defer known.Unlock()
	return append([]string(nil), known.addrs[name]...)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dialContext is the dialer's TCP connection: addr's name looked up within
// LookupTimeout and remembered, or, when that fails, its last known
// addresses. Each address gets an even share of what is left of ctx, so one
// that doesn't answer (a bridge node down) leaves time for the next.
func dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return netDial(ctx, network, addr)
	}
	lctx, cancel := context.WithTimeout(ctx, LookupTimeout)
	ips, lerr := resolver.LookupHost(lctx, host)
	cancel()
	if lerr == nil && len(ips) > 0 {
		remember(host, ips)
	} else if ips = lastKnown(host); len(ips) == 0 {
		if lerr == nil {
			lerr = errors.New("no addresses for " + host) // i18n-ignore: a dial error, logged only
		}
		return nil, lerr
	}
	var firstErr error
	for i, ip := range ips {
		actx, cancel := ctx, context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok {
			share := time.Until(deadline) / time.Duration(len(ips)-i)
			actx, cancel = context.WithTimeout(ctx, share)
		}
		c, err := netDial(actx, network, net.JoinHostPort(ip, port))
		cancel()
		if err == nil {
			return c, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, firstErr
}
