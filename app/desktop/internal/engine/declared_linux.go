// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package engine

import (
	"os"
	"path/filepath"
)

// declaredMountPoints are the mount points /etc/fstab and the systemd
// mount and automount units (the system's, and this user's) declare,
// mounted or not (declared.go).
func declaredMountPoints() []string {
	var out []string
	if data, err := os.ReadFile("/etc/fstab"); err == nil {
		out = append(out, fstabMountPoints(data)...)
	}
	dirs := []string{"/etc/systemd/system", "/run/systemd/system"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config", "systemd", "user"))
	}
	for _, dir := range dirs {
		for _, pattern := range []string{"*.mount", "*.automount"} {
			units, _ := filepath.Glob(filepath.Join(dir, pattern))
			for _, u := range units {
				if data, err := os.ReadFile(u); err == nil && len(data) < 1<<16 {
					if w := unitWhere(data); w != "" {
						out = append(out, w)
					}
				}
			}
		}
	}

	return out
}
