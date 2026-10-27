// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package tailscalefunnel

// ramDir: the device runs on Linux; elsewhere (a development Mac) there is
// no RAM filesystem to tell apart, and the key goes in argv as before.
func ramDir() string { return "" }
