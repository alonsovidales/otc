// SPDX-License-Identifier: AGPL-3.0-or-later

package tailscalefunnel

import (
	"os"
	"strings"
	"testing"
)

// The auth key reaches `tailscale up` as a file only this account can
// read, on a RAM filesystem, gone once the command is over: in argv every
// account on the machine could read it in /proc.
func TestAuthKeyGoesInAFile(t *testing.T) {
	dir := t.TempDir()
	arg, done, err := authKeyArg("tskey-auth-secret", dir)
	if err != nil {
		t.Fatal(err)
	}
	path, ok := strings.CutPrefix(arg, "--auth-key=file:")
	if !ok || strings.Contains(arg, "tskey-auth-secret") {
		t.Fatalf("argument %q: want the key's file, not the key", arg)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "tskey-auth-secret" {
		t.Fatalf("the file holds %q (%v)", b, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 || !strings.HasPrefix(path, dir) {
		t.Errorf("the file %s: %v, %v; want 0600 in %s", path, info.Mode(), err, dir)
	}
	done()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the key's file is still there: %v", err)
	}

	// Nowhere in RAM to write it: argv, as before.
	if arg, done, err := authKeyArg("tskey-auth-secret", ""); err != nil || arg != "--auth-key=tskey-auth-secret" {
		t.Errorf("without a RAM directory: %q, %v", arg, err)
	} else {
		done()
	}
}
