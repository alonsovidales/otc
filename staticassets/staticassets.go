// SPDX-License-Identifier: AGPL-3.0-or-later

// Package staticassets resolves a URL path against a device's own built
// web assets directory (web/dist, copied to [otc-api] static on deploy).
//
// Issue #95: the bridge used to keep its own separate copy of this same
// directory (bridge/static/), which had to be redeployed by hand every
// time a device's own web build changed - "old versions using the bridge"
// (a device that hasn't been redeployed yet) and "newer assets" (a bridge
// redeployed ahead of it, or vice versa) could each end up serving a
// mismatched bundle. The bridge now fetches assets from the device itself
// on every request instead (see ReqGetStaticAsset in proto/messages.proto
// and bridge/websocket's Manager.ForwardOneOff), so this is the one
// implementation both the device's own direct HTTP server and that RPC
// share - the one thing standing between a hostile path and the rest of
// the device's filesystem, now that it's reachable remotely through the
// bridge relay rather than only from a browser on the same LAN.
package staticassets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidPath is returned for anything that would resolve outside root,
// or that doesn't end up naming a real file inside it. Deliberately
// generic - never echoes the requested path back, so a caller answering a
// remote request (the bridge, on a device's behalf) has nothing path- or
// filesystem-shaped to leak in an error message.
var ErrInvalidPath = errors.New("invalid asset path")

// Resolve validates urlPath against root (a device's own built web assets
// directory) and returns the resolved absolute path to serve, applying
// the same rules an SPA-aware static file server needs: reject anything
// containing "..", append ".html" to an extension-less path so a client-
// side route (e.g. "/social") resolves to its matching page, and fall
// back to root's own index.html for anything that still doesn't resolve
// to a real file (a client-side route the ".html" guess didn't match, a
// captive-portal probe path, or just a typo - all the same "not a real
// file" case the SPA itself is meant to handle).
//
// The result is guaranteed to exist as a regular file strictly inside
// root - never a directory, and never anything outside root regardless of
// what urlPath asks for. This matters more than it would for an ordinary
// browser request now: urlPath arriving over the wire via
// ReqGetStaticAsset is an arbitrary attacker-controlled string with no
// URL-parser cleaning applied first, unlike net/http's ServeMux, which
// has already normalized r.URL.Path by the time a handler sees it. Two
// checks, not one: the leading ".." substring rejection is what actually
// stops every escape path is built from (this function only ever
// concatenates root+urlPath, never treats a leading "/" in urlPath as an
// absolute-path override, so a "clean" candidate - no ".." components at
// all - can never resolve outside root); the second, resolving both root
// and the candidate to absolute paths and confirming the candidate still
// sits inside root, is genuine defense in depth against a future change
// to how candidate gets built (e.g. swapping the concatenation above for
// filepath.Join, which normalizes ".." sequences at join time) silently
// reopening that gap.
func Resolve(root, urlPath string) (path string, err error) {
	if strings.Contains(urlPath, "..") {
		return "", ErrInvalidPath
	}

	candidate := root + urlPath
	lastSlash, lastDot := -1, -1
	for i := 0; i < len(candidate); i++ {
		switch candidate[i] {
		case '/':
			lastSlash = i
		case '.':
			lastDot = i
		}
	}
	if urlPath != "" && lastDot < lastSlash {
		candidate += ".html"
	}

	if info, statErr := os.Stat(candidate); statErr != nil || info.IsDir() {
		candidate = root + "index.html"
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}

	info, err := os.Stat(absPath)
	if err != nil || info.IsDir() {
		return "", ErrInvalidPath
	}

	return absPath, nil
}
