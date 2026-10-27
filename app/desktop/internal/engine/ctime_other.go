// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux && !darwin

package engine

import "os"

// changeTime is 0 - not checked - where a file's change time can't be read
// cheaply. Windows keeps one (ChangeTime), but only behind a handle opened
// on each file, which a pass over 100,000 files every minute can't afford;
// there the cache goes by size and modification time, as before.
func changeTime(os.FileInfo) int64 { return 0 }
