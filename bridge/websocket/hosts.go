// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"regexp"
	"strings"
	"time"

	"github.com/alonsovidales/otc/bridge/clientaddr"
)

// What a request for a device's address costs the shared database. Anyone
// can send any Host header, and every message of a socket not paired yet -
// and every GET for a device's address - asked MySQL whether that address
// was disabled, before any limit: a client sending tiny messages in a loop,
// or requests with made-up names, kept the primary and both nodes' pools
// busy, and the devices' registrations queued behind them.

// cDeviceLabel is the label of a device's address: what accounts.NamePattern
// allows, plus underscores, which a name an operator added by hand may hold.
var cDeviceLabel = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)

// IsDeviceHost reports whether host can be a device's address on a bridge
// for tld: one label under it, in any case. An IP address, a port, a name
// of another domain or of more labels - the certificate covers one - is no
// device's and is answered without a lookup. With no tld configured
// (development, tests) any host can be one.
func IsDeviceHost(host, tld string) bool {
	if tld == "" {
		return true
	}
	label, ok := strings.CutSuffix(strings.ToLower(host), "."+strings.ToLower(tld))
	return ok && cDeviceLabel.MatchString(label)
}

// cDisabledTTL is how long a device's disabled flag (issue #93) is taken
// as read: one set through this node shows at once, one set through
// another node within this. Var so tests can shrink it.
var cDisabledTTL = 5 * time.Second

// Lookups of the flag the memory doesn't answer: per client address, at
// most cLookupPerSecond a second after a burst of cLookupBurst - every
// device a client opens is one, a few at most.
const (
	cLookupPerSecond = 5
	cLookupBurst     = 30
	// cDisabledSweepAt: the remembered flags are swept of the expired
	// ones once there are this many (or twice as many as the last sweep
	// left).
	cDisabledSweepAt = 4096
)

type disabledFlag struct {
	disabled bool
	until    time.Time
}

// DeviceDisabled reports whether host's device is disabled, for a client
// at addr: from memory when it was read within cDisabledTTL, from the
// database only for a host that can be a device's (IsDeviceHost) and
// within the address's lookups. A lookup it doesn't make answers "not
// disabled", which only changes the words: the request then finds no
// connection for the device, as it would for a disabled one.
func (mg *Manager) DeviceDisabled(addr, host string) (bool, error) {
	if mg.dao == nil || !IsDeviceHost(host, mg.tld) {
		return false, nil
	}
	now := time.Now()
	mg.disabledMu.Lock()
	f, ok := mg.disabled[host]
	mg.disabledMu.Unlock()
	if ok && now.Before(f.until) {
		return f.disabled, nil
	}
	if mg.lookupPerAddr != nil && !mg.lookupPerAddr.Allow(clientaddr.Bucket(addr)) {
		return false, nil
	}
	disabled, err := mg.dao.IsDeviceDisabled(host)
	if err != nil {
		return false, err
	}
	mg.rememberDisabled(host, disabled, now)
	return disabled, nil
}

// rememberDisabled keeps host's flag for cDisabledTTL from now.
func (mg *Manager) rememberDisabled(host string, disabled bool, now time.Time) {
	mg.disabledMu.Lock()
	defer mg.disabledMu.Unlock()
	if mg.disabled == nil {
		mg.disabled = map[string]disabledFlag{}
	}
	if len(mg.disabled) >= max(cDisabledSweepAt, 2*mg.disabledKept) {
		for h, f := range mg.disabled {
			if !now.Before(f.until) {
				delete(mg.disabled, h)
			}
		}
		mg.disabledKept = len(mg.disabled)
	}
	mg.disabled[host] = disabledFlag{disabled: disabled, until: now.Add(cDisabledTTL)}
}
