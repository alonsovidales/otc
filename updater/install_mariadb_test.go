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

// install.sh's mariadb_datadir makes /mnt/storage/mysql MariaDB's datadir,
// seeding it from the package's own, as root - in the storage root the otc
// user owns, while the otc service keeps running. The installer used to
// check the name for a symlink, then stop MariaDB (seconds), then copy into
// the name: a link put there in between had root write MariaDB's files
// into its target and give that target the datadir's owner and mode. Run as
// root on Linux only, with nobody standing in for otc and daemon for mysql:
// `sudo go test -run MariaDB`.
func TestMariaDBDatadirNeverFollowsAName(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("needs root on Linux")
	}
	for _, name := range []string{"nobody", "daemon"} {
		if _, err := user.Lookup(name); err != nil {
			t.Skipf("no %s account", name)
		}
	}
	for _, tool := range []string{"runuser", "rsync"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}
	install, err := os.ReadFile("../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?ms)^mariadb_datadir\(\) \{\n.*?^\}\n`).FindString(string(install))
	if fn == "" {
		t.Fatal("scripts/install.sh has no mariadb_datadir")
	}
	call := regexp.MustCompile(`(?m)^\s*mariadb_datadir "\$MOUNT_POINT/mysql" /var/lib/mysql mysql \\$`)
	if !call.Match(install) {
		t.Fatal("scripts/install.sh no longer sets up the datadir with mariadb_datadir")
	}

	// Not t.TempDir(): nobody has to reach it, and that one's parent is 0700.
	root, err := os.MkdirTemp("", "otc-mariadb-test.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// The package's datadir, as Debian leaves it: mysql's, 0755, its files
	// 0660.
	seed := filepath.Join(root, "seed")
	prepare := exec.Command("/bin/bash", "-c", `set -e
mkdir -p "$S/mysql"; echo pkg > "$S/ibdata1"; echo pkg > "$S/mysql/user.frm"
chmod 0755 "$S" "$S/mysql"; chmod 0660 "$S/ibdata1" "$S/mysql/user.frm"
chown -R daemon:daemon "$S"`)
	prepare.Env = append(os.Environ(), "S="+seed)
	if out, err := prepare.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	// run: mariadb_datadir on $P/mysql, in a storage root ($P) of nobody's,
	// after setup; a root directory ($V) is what a planted link points at.
	// SWAP_AT names a command the script runs (mkdir, rsync) that a link
	// takes the directory's place just before - as if the otc user did it
	// at that moment.
	n := 0
	run := func(setup, swapAt string) (parent, victim, out string) {
		t.Helper()
		n++
		parent = filepath.Join(root, "storage"+strings.Repeat("x", n))
		victim = filepath.Join(root, "victim"+strings.Repeat("x", n))
		for _, d := range []string{parent, victim} {
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		script := `set -uo pipefail
swap() {
    [ "$SWAP_AT" = "$1" ] || return 0
    SWAP_AT=done
    runuser -u nobody -- sh -c 'mv "$1/mysql" "$1/mysql.moved" && ln -s "$2" "$1/mysql"' _ "$P" "$V"
}
mkdir() { command mkdir "$@"; local rc=$?; swap mkdir; return "$rc"; }
rsync() { swap rsync; command rsync "$@"; }
` + fn + `
chown nobody: "$P"; chmod 0755 "$P"
` + setup + `
mariadb_datadir "$P/mysql" "$S" daemon
echo "rc=$?"
`
		cmd := exec.Command("/bin/bash", "-c", script)
		cmd.Env = append(os.Environ(), "P="+parent, "V="+victim, "S="+seed, "SWAP_AT="+swapAt)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return parent, victim, string(b)
	}
	// untouched: the root directory a link pointed at is as it was - root's,
	// 0700, empty.
	untouched := func(what, victim, out string) {
		t.Helper()
		st, err := os.Stat(victim)
		if err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(victim)
		if st.Mode().Perm() != 0o700 || len(entries) != 0 || fileOwner(t, victim) != "root" {
			t.Errorf("%s: the link's target became %s %o with %d entries\n%s", what, fileOwner(t, victim), st.Mode().Perm(), len(entries), out)
		}
	}
	seeded := func(what, dir, out string) {
		t.Helper()
		for _, f := range []string{"", "ibdata1", "mysql", "mysql/user.frm"} {
			if got := fileOwner(t, filepath.Join(dir, f)); got != "daemon" {
				t.Errorf("%s: %s/%s is %s's, want daemon's\n%s", what, dir, f, got, out)
			}
		}
	}

	// A fresh array: seeded, MariaDB's.
	parent, _, out := run("", "")
	if !strings.Contains(out, "rc=0") {
		t.Fatalf("fresh: failed\n%s", out)
	}
	seeded("fresh", filepath.Join(parent, "mysql"), out)

	// A recovered array's datadir: kept as it is, handed to MariaDB.
	parent, _, out = run(`mkdir -p "$P/mysql/mysql"; echo old > "$P/mysql/mysql/user.frm"`, "")
	if !strings.Contains(out, "rc=0") {
		t.Fatalf("recovered: failed\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(parent, "mysql/mysql/user.frm")); string(b) != "old\n" {
		t.Errorf("recovered: its user.frm became %q\n%s", b, out)
	}
	if _, err := os.Stat(filepath.Join(parent, "mysql/ibdata1")); err == nil {
		t.Errorf("recovered: seeded over\n%s", out)
	}
	if got := fileOwner(t, filepath.Join(parent, "mysql/mysql/user.frm")); got != "daemon" {
		t.Errorf("recovered: user.frm is %s's\n%s", got, out)
	}

	// An earlier run's copy, cut short: seeded again.
	parent, _, out = run(`mkdir "$P/mysql"; echo half > "$P/mysql/ibdata1"; chown -R daemon: "$P/mysql"; chmod 0660 "$P/mysql/ibdata1"`, "")
	if !strings.Contains(out, "rc=0") {
		t.Fatalf("half-copied: failed\n%s", out)
	}
	seeded("half-copied", filepath.Join(parent, "mysql"), out)

	// A link put there before: refused, nothing written through it.
	_, victim, out := run(`runuser -u nobody -- ln -s "$V" "$P/mysql"`, "")
	if strings.Contains(out, "rc=0") {
		t.Errorf("a planted link: accepted\n%s", out)
	}
	untouched("a planted link", victim, out)

	// The directory swapped for a link once it was made (the name checked,
	// then used): refused, nothing written through it.
	_, victim, out = run("", "mkdir")
	if strings.Contains(out, "rc=0") {
		t.Errorf("swapped after mkdir: accepted\n%s", out)
	}
	untouched("swapped after mkdir", victim, out)

	// Swapped while root works in it: the work goes on in the directory
	// root is in, never into the link.
	parent, victim, out = run("", "rsync")
	untouched("swapped before the copy", victim, out)
	if !strings.Contains(out, "rc=0") {
		t.Errorf("swapped before the copy: failed\n%s", out)
	}
	seeded("swapped before the copy", filepath.Join(parent, "mysql.moved"), out)

	// A directory of the otc user's own making, with its files and no
	// datadir: refused, nothing copied into it.
	parent, _, out = run(`runuser -u nobody -- mkdir "$P/mysql"; runuser -u nobody -- mkdir "$P/mysql/performance_schema"`, "")
	if strings.Contains(out, "rc=0") {
		t.Errorf("the otc user's own directory: seeded\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(parent, "mysql/ibdata1")); err == nil {
		t.Errorf("the otc user's own directory: copied into\n%s", out)
	}
}

func fileOwner(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("stat", "-c", "%U", path).Output()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}
