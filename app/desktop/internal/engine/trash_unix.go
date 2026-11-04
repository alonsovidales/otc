// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix && !darwin

package engine

import "time"

// systemTrash moves p to the freedesktop.org trash (trash_xdg.go).
func systemTrash(p string) error { return freedesktopTrash(p, time.Now()) }
