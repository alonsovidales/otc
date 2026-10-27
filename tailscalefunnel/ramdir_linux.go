// SPDX-License-Identifier: AGPL-3.0-or-later

package tailscalefunnel

import (
	"os"

	"golang.org/x/sys/unix"
)

// ramDir is where authKeyArg writes an auth key: the process's own
// temporary directory (hardening.SecureTempDir's, on a RAM filesystem),
// else the service's runtime directory, if either is a tmpfs; "" when
// neither is.
func ramDir() string {
	for _, dir := range []string{os.TempDir(), os.Getenv("RUNTIME_DIRECTORY")} {
		var st unix.Statfs_t
		if dir != "" && unix.Statfs(dir, &st) == nil && st.Type == unix.TMPFS_MAGIC {
			return dir
		}
	}
	return ""
}
