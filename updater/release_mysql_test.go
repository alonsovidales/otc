// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeMysql stands in for the mysql client the release scripts run (as
// root, on a device): it writes each call's arguments to FAKE_CALLS, one
// per line and the call ended by a line "--" (and "<stdin>" when SQL came
// on its standard input), and answers what the scripts ask - the users
// list (FAKE_USERS, or FAKE_USERS_ERROR with status 1, or failing only
// the first time when FAKE_USERS_FAIL_ONCE names a file not there yet),
// whether a database exists (FAKE_DBS, or FAKE_DBS_ERROR with status 1, or
// failing only the first time when FAKE_DBS_FAIL_ONCE names a file not
// there yet) and whether the users table does.
const fakeMysql = `#!/bin/bash
stmt=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
    if [ "${args[i]}" = -e ]; then stmt="${args[i+1]:-}"; fi
done
{
    for a in "$@"; do printf '%s\n' "$a"; done
    if [ -z "$stmt" ] && [ -n "$(cat)" ]; then echo "<stdin>"; fi
    echo --
} >>"$FAKE_CALLS"
case "$stmt" in
    "SELECT db_name FROM users")
        if [ -n "${FAKE_USERS_FAIL_ONCE:-}" ] && [ ! -e "$FAKE_USERS_FAIL_ONCE" ]; then
            : >"$FAKE_USERS_FAIL_ONCE"
            echo "ERROR 2013 (HY000): Lost connection to server during query" >&2
            exit 1
        fi
        if [ -n "${FAKE_USERS_ERROR:-}" ]; then echo "$FAKE_USERS_ERROR" >&2; exit 1; fi
        printf '%s\n' "$FAKE_USERS"
        ;;
    "SHOW DATABASES LIKE '"*)
        if [ -n "${FAKE_DBS_FAIL_ONCE:-}" ] && [ ! -e "$FAKE_DBS_FAIL_ONCE" ]; then
            : >"$FAKE_DBS_FAIL_ONCE"
            echo "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)" >&2
            exit 1
        fi
        if [ -n "${FAKE_DBS_ERROR:-}" ]; then echo "$FAKE_DBS_ERROR" >&2; exit 1; fi
        name="${stmt#"SHOW DATABASES LIKE '"}"
        name="${name%"'"}"
        printf '%s\n' "$FAKE_DBS" | grep -xF -- "$name" || true
        ;;
    "SHOW TABLES LIKE 'users'") echo users ;;
esac
exit 0
`

// releaseRun is one run of run_release (scripts/update.sh's own function,
// taken from the file) on a script, with fakeMysql as the real client.
type releaseRun struct {
	rc     string
	calls  [][]string
	output string
}

func runRelease(t *testing.T, version, script string, env ...string) releaseRun {
	t.Helper()
	update, err := os.ReadFile("../scripts/update.sh")
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?ms)^run_release\(\) \{\n.*?^\}\n`).Find(update)
	if fn == nil {
		t.Fatal("scripts/update.sh has no run_release")
	}
	script, err = filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	fake := filepath.Join(work, "fake-mysql")
	if err := os.WriteFile(fake, []byte(fakeMysql), 0o755); err != nil {
		t.Fatal(err)
	}
	// As update.sh stages it: readable, not executable.
	guard := filepath.Join(work, "release-mysql.sh")
	src, err := os.ReadFile("../scripts/update-runner/release-mysql.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, src, 0o644); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(work, "calls")
	empty := filepath.Join(work, "cwd") // where a name like * would glob
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	harness := "set -u\ntmp=\"$WORK\"\nguard=\"$GUARD\"\nreal_mysql=\"$FAKE\"\n" + string(fn) +
		"run_release \"$VERSION\" \"$SCRIPT\"\necho \"rc=$?\"\n"
	cmd := exec.Command("/bin/bash", "-c", harness)
	cmd.Dir = empty
	cmd.Env = append(os.Environ(), "WORK="+work, "GUARD="+guard, "FAKE="+fake, "FAKE_CALLS="+calls,
		"VERSION="+version, "SCRIPT="+script)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	r := releaseRun{output: string(out)}
	if m := regexp.MustCompile(`(?m)^rc=(\d+)$`).FindStringSubmatch(r.output); m != nil {
		r.rc = m[1]
	}
	raw, _ := os.ReadFile(calls)
	var call []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "--" {
			r.calls = append(r.calls, call)
			call = nil
		} else if line != "" {
			call = append(call, line)
		}
	}
	return r
}

const goodDB = "otc_0123456789abcdef0123456789abcdef"

// The released scripts before 92 passed every users.db_name - a column the
// otc service can write - to root's mysql as its first argument, where
// --defaults-extra-file= makes the client read a file of that user's. Run
// with update.sh's mysql, only otc and a user's existing otc_<32 hex>
// database ever reach the client, whatever the table holds; every other
// release before 92 that loops over the users is run the same way.
func TestReleasesBefore92PassOnlyTheDeviceDatabaseNames(t *testing.T) {
	users := strings.Join([]string{
		"--defaults-extra-file=/var/lib/otc/m.cnf",
		"-hevil.example",
		goodDB,
		"otc_ffffffffffffffffffffffffffffffff", // well formed, no such database
		"otc_0123456789ABCDEF0123456789ABCDEF",
		"otc_x --plugin-dir=/var/lib/otc/p",
		"*",
		"mysql",
	}, "\n")
	for _, v := range []string{"1", "3", "7", "8", "12", "24", "27", "35", "38", "43", "51", "60", "69", "73", "74"} {
		r := runRelease(t, v, "../scripts/updates/"+v+".sh", "FAKE_USERS="+users, "FAKE_DBS="+goodDB)
		if r.rc != "0" {
			t.Errorf("release %s: run_release = %q, want 0\n%s", v, r.rc, r.output)
			continue
		}
		applied := map[string]bool{}
		for _, call := range r.calls {
			for i := 0; i < len(call); i++ {
				a := call[i]
				switch {
				case a == "-e":
					i++
				case a == "-N" || a == "-B" || a == "<stdin>":
				case strings.HasPrefix(a, "-"):
					t.Errorf("release %s: the client got the option %q (call %q)", v, a, call)
				case a != "otc" && a != goodDB:
					t.Errorf("release %s: the client got the database %q (call %q)", v, a, call)
				}
			}
			if len(call) == 2 && call[1] == "<stdin>" {
				applied[call[0]] = true
			}
		}
		if !applied["otc"] || !applied[goodDB] || len(applied) != 2 {
			t.Errorf("release %s: SQL went to %v, want otc and %s", v, applied, goodDB)
		}
	}
}

// What the guard is for: the same script with the client in its place
// hands it the otc service's option.
func TestRelease74UnguardedPassesTheOption(t *testing.T) {
	work := t.TempDir()
	fake := filepath.Join(work, "mysql")
	if err := os.WriteFile(fake, []byte(fakeMysql), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(work, "calls")
	cmd := exec.Command("/bin/bash", "../scripts/updates/74.sh")
	cmd.Env = append(os.Environ(), "PATH="+work+":"+os.Getenv("PATH"), "FAKE_CALLS="+calls,
		"FAKE_USERS=--defaults-extra-file=/var/lib/otc/m.cnf")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("74.sh: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(calls)
	if !strings.Contains(string(raw), "\n--defaults-extra-file=/var/lib/otc/m.cnf\n") {
		t.Fatalf("the unguarded script never passed the option - is this test still testing anything?\n%s", raw)
	}
}

// The users list read with 2>/dev/null || true: a database that didn't
// answer read as "no other users", and the release was recorded as
// applied with every other user's database left behind. Now such a
// release runs again once the database answers, and the update stops (2)
// when it still can't be read; a device with no users table (error 1146)
// simply has none.
func TestReleasesStopWhenTheUsersCantBeRead(t *testing.T) {
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	noTable := "ERROR 1146 (42S02) at line 1: Table 'otc.users' doesn't exist"
	for _, v := range []string{"1", "74", "92", "93", "108", "118", "148", "152", "157", "163"} {
		script := "../scripts/updates/" + v + ".sh"
		if r := runRelease(t, v, script, "FAKE_USERS_ERROR="+down); r.rc != "2" {
			t.Errorf("release %s with the database down: run_release = %q, want 2\n%s", v, r.rc, r.output)
		}
		if r := runRelease(t, v, script, "FAKE_USERS_ERROR="+noTable); r.rc != "0" {
			t.Errorf("release %s with no users table: run_release = %q, want 0\n%s", v, r.rc, r.output)
		}
		once := filepath.Join(t.TempDir(), "failed-once")
		r := runRelease(t, v, script, "FAKE_USERS="+goodDB, "FAKE_DBS="+goodDB, "FAKE_USERS_FAIL_ONCE="+once)
		if r.rc != "0" {
			t.Errorf("release %s, the users read failing once: run_release = %q, want 0\n%s", v, r.rc, r.output)
		}
		reached := false
		for _, call := range r.calls {
			if len(call) == 2 && call[0] == goodDB && call[1] == "<stdin>" {
				reached = true
			}
		}
		if !reached {
			t.Errorf("release %s, run again: %s's SQL never ran\n%s", v, goodDB, r.output)
		}
	}
}

// The users list read, then the database stopped answering (MariaDB
// restarted by a security update half-way through the loop): from release
// 92 on, the scripts asked whether each user's database exists through a
// pipe into grep, so every user left read as "no such database" and the
// release was recorded as applied without them. Now that failure is noted
// like a failed users list: the release runs again once the database
// answers, and the update stops (2) when it still can't be asked. Before
// 92 the guard asks itself, and notes it the same way.
func TestReleasesStopWhenADatabaseCantBeChecked(t *testing.T) {
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	// 168 also drives Tailscale, which a test has none of; 169 adds its
	// month counts again and 170 turns Places on, read the same way.
	scripts := map[string]string{"158": "../scripts/updates/corrected/158.sh"}
	for _, v := range []string{"1", "74", "92", "93", "108", "118", "148", "152", "157", "163", "169", "170"} {
		scripts[v] = "../scripts/updates/" + v + ".sh"
	}
	for v, script := range scripts {
		// 169's network section is the host's in a container: left alone.
		r := runRelease(t, v, script, "OTC_CONTAINER=1", "FAKE_USERS="+goodDB, "FAKE_DBS_ERROR="+down)
		if r.rc != "2" {
			t.Errorf("release %s, the database down after the users list: run_release = %q, want 2\n%s", v, r.rc, r.output)
		}
		once := filepath.Join(t.TempDir(), "failed-once")
		r = runRelease(t, v, script, "OTC_CONTAINER=1", "FAKE_USERS="+goodDB, "FAKE_DBS="+goodDB, "FAKE_DBS_FAIL_ONCE="+once)
		if r.rc != "0" {
			t.Errorf("release %s, the existence check failing once: run_release = %q, want 0\n%s", v, r.rc, r.output)
		}
		reached := false
		for _, call := range r.calls {
			if len(call) == 2 && call[0] == goodDB && call[1] == "<stdin>" {
				reached = true
			}
		}
		if !reached {
			t.Errorf("release %s, run again: %s's SQL never ran\n%s", v, goodDB, r.output)
		}
	}
}

// From release 92 on, the scripts check the names themselves, and their
// calls reach the client exactly as written, standard input included; a
// script that fails is a failure (1).
func TestLaterReleasesReachTheClientUnchanged(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "170.sh")
	body := "set -euo pipefail\nmysql --batch -u root -e 'SELECT 1' otc\nmysql otc <<'SQL'\nSELECT 2;\nSQL\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runRelease(t, "170", script)
	want := fmt.Sprint([][]string{{"--batch", "-u", "root", "-e", "SELECT 1", "otc"}, {"otc", "<stdin>"}})
	if r.rc != "0" || fmt.Sprint(r.calls) != want {
		t.Errorf("run_release = %q with calls %v, want 0 and %s\n%s", r.rc, r.calls, want, r.output)
	}
	failing := filepath.Join(dir, "171.sh")
	if err := os.WriteFile(failing, []byte("exit 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runRelease(t, "171", failing); r.rc != "1" {
		t.Errorf("a failing script: run_release = %q, want 1", r.rc)
	}
}
