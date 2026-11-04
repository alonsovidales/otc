// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package engine

import "os"

// fileSystemOf: not read on Windows, where a volume mounted in a folder is
// a junction, which the scan never enters (it isn't a plain folder).
func fileSystemOf(os.FileInfo) (uint64, bool) { return 0, false }
