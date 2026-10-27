// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package engine

import "os"

// hiddenAttr: no hidden attribute outside Windows (a dot name is hidden).
func hiddenAttr(os.FileInfo) bool { return false }
