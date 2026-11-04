// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// install.sh's otc_dir makes a directory of the otc user's inside a
// directory that user owns (/var/lib/otc/users, the storage's unencrypted
// directory) without ever following what is there: a re-run of the
// installer used to chown and chmod a symlink the otc user had put in its
// place, handing it the link's target - /var/lib/dpkg/info, say. Run as
// root on Linux only (with nobody standing in for otc): `sudo go test`.
func TestOtcDirNeverFollowsALink(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("needs root on Linux")
	}
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("no nobody account")
	}
	if _, err := exec.LookPath("runuser"); err != nil {
		t.Skip("no runuser")
	}
	install, err := os.ReadFile("../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?ms)^otc_dir\(\) \{\n.*?^\}\n`).FindString(string(install))
	if fn == "" {
		t.Fatal("scripts/install.sh has no otc_dir")
	}
	group := "nogroup"
	if _, err := user.LookupGroup(group); err != nil {
		group = "nobody"
	}
	fn = strings.NewReplacer("-u otc", "-u nobody", "!= otc ]", "!= nobody ]", "otc:otc", "nobody:"+group).Replace(fn)

	// Not t.TempDir(): nobody has to reach it, and that one's parent is 0700.
	root, err := os.MkdirTemp("", "otc-dir-test.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	parent, victim := filepath.Join(root, "otc-owned"), filepath.Join(root, "roots")
	for _, d := range []string{parent, victim} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script := `set -euo pipefail
log() { :; }
die() { echo "$*" >&2; exit 1; }
` + fn + `
chown nobody: "$P"; chmod 0755 "$P"
runuser -u nobody -- ln -s "$V" "$P/users"
runuser -u nobody -- ln -s "$V" "$P/unencrypted"
runuser -u nobody -- touch "$P/file"
mkdir "$P/roots-old"
otc_dir "$P/users" 0755
otc_dir "$P/unencrypted/" 0750
otc_dir "$P/file" 0750
otc_dir "$P/roots-old" 0755
otc_dir "$P/new" 0750
stat -c '%n %U %a %F' "$V" "$P/users" "$P/unencrypted" "$P/file" "$P/roots-old" "$P/new"
`
	cmd := exec.Command("/bin/bash", "-c", script)
	cmd.Env = append(os.Environ(), "P="+parent, "V="+victim)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := strings.Join([]string{
		victim + " root 700 directory",
		parent + "/users nobody 755 directory",
		parent + "/unencrypted nobody 750 directory",
		parent + "/file nobody 750 directory",
		parent + "/roots-old nobody 755 directory",
		parent + "/new nobody 750 directory",
	}, "\n") + "\n"
	if string(out) != want {
		t.Errorf("after otc_dir:\n%s\nwant\n%s", out, want)
	}
}
