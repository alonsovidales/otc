// SPDX-License-Identifier: AGPL-3.0-or-later

package diskspace

import "golang.org/x/sys/unix"

func of(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	return fromStatfs(st.Blocks, st.Bfree, st.Bavail, uint64(st.Frsize)), nil
}
