// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package oslang

import "os"

// preferred reads the gettext environment the desktop session sets (Linux;
// on a Mac, where this Go build only runs during development, a terminal's
// LANG).
func preferred() []string { return fromEnv(os.Getenv) }
