// SPDX-License-Identifier: AGPL-3.0-or-later

package devtext

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

// The runners' scripts and the table each one's codes are read with.
var runnerTables = map[string]map[string]string{
	"update": updateCodes, "bridge": bridgeCodes, "tailscale": tailscaleCodes, "wifi": wifiRestartCodes,
}

var runnerScripts = []struct{ path, table string }{
	{"../scripts/update-runner/otc-update-runner.sh", "update"},
	{"../scripts/update-runner/otc-update-stopped.sh", "update"},
	{"../scripts/update.sh", "update"},
	{"../scripts/bridge-runner/otc-bridge-runner.sh", "bridge"},
	{"../scripts/tailscale-runner/otc-tailscale-runner.sh", "tailscale"},
	{"../scripts/device-runner/otc-wifi-restart-runner.sh", "wifi"},
}

var (
	// status <state> "<message>" ... or fail "<message>" ...
	reStatusCall = regexp.MustCompile(`\b(?:status [a-z]+|fail) "((?:[^"\\]|\\.)*)"(.*)$`)
	// what follows the message: a code, then maybe "$(args name "value" ...)"
	reCodeArgs = regexp.MustCompile(`^ ([a-z_]+)(?: "\$\(args ((?:[a-z_]+ "[^"]*" ?)+)\)")?(?:[ ;)|].*)?$`)
	reArgPair  = regexp.MustCompile(`([a-z_]+) "([^"]*)"`)
	// ${NAME} and $NAME are the same expansion.
	reBraced = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// Words a runner writes as its message that are tokens, not text: the
// bridge runner's "off" reason (bridgeaccess.LeftReason reads it) and the
// Tailscale runner's on/off answers. They carry no code.
var tokenMessages = map[string]bool{`$reason`: true, `on`: true, `off`: true}

// Every status line the runners write carries a code its reader knows,
// with exactly the arguments the code's key declares, and the key's
// English with those arguments is the message the script writes next to
// it, character for character - the proof that a device reading the
// code says what the runner said. Every code a table lists is written by
// some script.
func TestRunnerScriptsMatchTheCatalog(t *testing.T) {
	used := map[string]map[string]bool{}
	calls := 0
	for _, rs := range runnerScripts {
		table := runnerTables[rs.table]
		if used[rs.table] == nil {
			used[rs.table] = map[string]bool{}
		}
		raw, err := os.ReadFile(rs.path)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			m := reStatusCall.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			message, rest := m[1], m[2]
			if strings.HasPrefix(message, "$1") || tokenMessages[message] {
				continue // status() forwarding fail()'s arguments, or a token
			}
			calls++
			where := fmt.Sprintf("%s:%d", rs.path, n+1)
			cm := reCodeArgs.FindStringSubmatch(rest)
			if cm == nil {
				t.Errorf("%s: %q has no code", where, message)
				continue
			}
			code := cm[1]
			key, ok := table[code]
			if !ok {
				t.Errorf("%s: code %q is not in the %s table", where, code, rs.table)
				continue
			}
			used[rs.table][code] = true
			args := map[string]any{}
			for _, p := range reArgPair.FindAllStringSubmatch(cm[2], -1) {
				args[p[1]] = p[2]
			}
			blob, _ := json.Marshal(args)
			if _, err := i18n.ParseStored(key, blob); err != nil {
				t.Errorf("%s: code %q with args %s doesn't fit %s: %v", where, code, blob, key, err)
				continue
			}
			got := stripIsolation(i18n.T("en", key, args))
			if norm(got) != norm(unescape(message)) {
				t.Errorf("%s: %s in English is %q, the script writes %q", where, key, got, message)
			}
		}
	}
	if calls < 60 {
		t.Errorf("only %d status lines found: has the scripts' syntax changed under this test?", calls)
	}
	for name, table := range runnerTables {
		for code, key := range table {
			if !used[name][code] {
				t.Errorf("code %q (%s) is written by no script", code, key)
			}
		}
	}
}

func norm(s string) string { return reBraced.ReplaceAllString(s, `$$$1`) }

// unescape undoes the shell's \" inside a double-quoted message.
func unescape(s string) string { return strings.ReplaceAll(s, `\"`, `"`) }

func stripIsolation(s string) string {
	return strings.NewReplacer("⁨", "", "⁩", "").Replace(s)
}
