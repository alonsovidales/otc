// SPDX-License-Identifier: AGPL-3.0-or-later

package hardening

import (
	"path/filepath"
	"strconv"
	"strings"
)

// tempBaseCandidate is the first place SecureTempDir looks for its base:
// TMPDIR, unless that is another otc process's own otc-<pid> directory -
// a supervised child inherits the primary's TMPDIR. Then it is that
// directory's base, so the child's directory is a sibling and its sweep
// never removes the primary's live temporary files.
func tempBaseCandidate(tmp string) string {
	if n := filepath.Base(tmp); strings.HasPrefix(n, "otc-") {
		if _, err := strconv.Atoi(strings.TrimPrefix(n, "otc-")); err == nil {
			return filepath.Dir(tmp)
		}
	}
	return tmp
}
