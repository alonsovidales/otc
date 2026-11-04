// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
)

// The mount points the system declares - /etc/fstab, systemd's mount and
// automount units - mounted or not (declaredMountPoints, Linux only). At
// the first pass of a record from before mounts were kept, one inside the
// folder that is an empty folder now is held as maybe a disk that isn't
// mounted (maybeMounts), however recently that folder changed.

// fstabMountPoints are the mount points (second field) of an fstab file,
// its octal escapes (\040 for a space) undone; swap and "none" left out.
func fstabMountPoints(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if p := fstabUnescape(fields[1]); filepath.IsAbs(p) {
			out = append(out, filepath.Clean(p))
		}
	}

	return out
}

// fstabUnescape undoes fstab's \ooo escapes.
func fstabUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3

				continue
			}
		}
		b.WriteByte(s[i])
	}

	return b.String()
}

// unitWhere is the Where= of a systemd mount or automount unit's [Mount]
// or [Automount] section, "" when it has none.
func unitWhere(data []byte) string {
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "["):
			section = line
		case section == "[Mount]" || section == "[Automount]":
			if v, ok := strings.CutPrefix(line, "Where="); ok {
				if p := strings.TrimSpace(v); filepath.IsAbs(p) {
					return filepath.Clean(p)
				}
			}
		}
	}

	return ""
}

// declaredUnder are the declared mount points strictly inside root, as
// keys relative to it.
func declaredUnder(root string, points []string) []string {
	root = filepath.Clean(root)
	var out []string
	for _, p := range points {
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			continue
		}
		out = append(out, pathKey(filepath.ToSlash(rel)))
	}

	return out
}
