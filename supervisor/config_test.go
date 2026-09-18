// SPDX-License-Identifier: AGPL-3.0-or-later

package supervisor

import "testing"

// Issue #103: "request bridge access" has to mean something beyond a
// checkbox, and this is where it becomes real - an empty bridge-addr in a
// user's own config is what stops that instance dialing the relay at all
// (websocket.Init skips the connection pool entirely when it reads one).
//
// cfg is deliberately never initialized in this test binary, which makes
// the assertion sharper than a string comparison: cfg.GetStr is fatal
// before cfg.Init, so a local-only user that returned anything other than
// early would take this test process down. Passing proves the flag is
// checked *first*, and that a local-only user's address can therefore
// never be inherited from whatever the primary happens to be configured
// with.
func TestBridgeAddrForIsEmptyForALocalOnlyUserWithoutConsultingConfig(t *testing.T) {
	if got := bridgeAddrFor(renderParams{BridgeAccess: false}); got != "" {
		t.Errorf("bridgeAddrFor(local-only) = %q, want empty so the instance never dials the relay", got)
	}
}
