// SPDX-License-Identifier: AGPL-3.0-or-later

// Package browser opens a web address in the user's default browser - what
// NSWorkspace.open does for the macOS app's "Open Web App" (issue #193).
package browser

import (
	"fmt"
	"net/url"
)

// Open opens addr, which must be an http or https address: nothing else
// is handed to the desktop's opener.
func Open(addr string) error {
	u, err := url.Parse(addr)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("not a web address: %q", addr)
	}

	return open(u.String())
}
