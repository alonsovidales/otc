// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The scripts that download a release's files from GitHub (the source
// archive, the web app, a pinned tool or model) carry one gh_api_fetch,
// byte for byte, which fetches a file through GitHub's API when its
// download link fails: on 2026-11-09 the links answered 504 for over ten
// minutes for some files while the API served them, and every update in
// that time failed with "could not download the source of release 177".
var ghFetchScripts = []string{
	"../scripts/update-runner/otc-update-runner.sh",
	"../scripts/update.sh",
	"../scripts/verified-install.sh",
	"../scripts/install.sh",
}

func shellFunc(t *testing.T, script, name string) string {
	t.Helper()
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?ms)^` + name + `\(\) \{\n.*?^\}\n`).FindString(string(b))
	if fn == "" {
		t.Fatalf("%s has no %s", script, name)
	}
	return fn
}

func TestGhAPIFetchIsTheSameEverywhere(t *testing.T) {
	want := shellFunc(t, ghFetchScripts[0], "gh_api_fetch")
	for _, s := range ghFetchScripts[1:] {
		if got := shellFunc(t, s, "gh_api_fetch"); got != want {
			t.Errorf("%s's gh_api_fetch differs from %s's:\n%s", s, ghFetchScripts[0], got)
		}
	}
}

// No release file is fetched with a bare curl any more: through gh_fetch,
// or (install.sh's fetch_pinned and fetch_optional, which take any URL)
// with gh_api_fetch after their curl. Every line naming a release file -
// continued lines joined, the functions themselves and comments left out -
// is an assignment or one of those calls, and each script has the calls
// it should.
func TestReleaseDownloadsFallBackToTheAPI(t *testing.T) {
	funcs := regexp.MustCompile(`(?ms)^gh_(api_)?fetch\(\) \{\n.*?^\}\n`)
	continued := regexp.MustCompile(`\\\n\s*`)
	names := regexp.MustCompile(`releases/download|QUALITY_ONNX_URL`)
	allowed := regexp.MustCompile(`^(?:if (?:! )?)?(gh_fetch|fetch_pinned|fetch_optional) |^[A-Z_]+=`)
	want := map[string]map[string]int{
		"../scripts/update-runner/otc-update-runner.sh": {"gh_fetch": 1},
		"../scripts/update.sh":                          {"gh_fetch": 4},
		"../scripts/verified-install.sh":                {"gh_fetch": 2},
		"../scripts/install.sh":                         {"fetch_pinned": 2, "fetch_optional": 1},
	}
	for _, s := range ghFetchScripts {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		text := continued.ReplaceAllString(funcs.ReplaceAllString(string(b), ""), " ")
		got := map[string]int{}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || !names.MatchString(line) {
				continue
			}
			m := allowed.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("%s fetches a release file without the API fallback: %s", s, line)
				continue
			}
			if m[1] != "" {
				got[m[1]]++
			}
		}
		for fn, n := range want[s] {
			if got[fn] != n {
				t.Errorf("%s fetches %d release files with %s, want %d", s, got[fn], fn, n)
			}
		}
	}
	install := shellFunc(t, "../scripts/install.sh", "fetch_pinned") + shellFunc(t, "../scripts/install.sh", "fetch_optional")
	if n := strings.Count(install, `gh_api_fetch "$url" "$tmpd/download"`); n != 2 {
		t.Errorf("install.sh's fetch_pinned and fetch_optional fall back to gh_api_fetch %d times, not 2", n)
	}
}

// A stand-in curl: the download link answers when $LINK is "ok", the API's
// release answers $TAGS (into a file: a pipe would keep a cut-short
// answer ahead of a retry's), and an asset answers only when asked for its
// bytes. Every call is logged to $CURL_LOG as "<url> <headers>".
const fakeCurl = `curl() {
    local out="" url="" hdr=""
    while [ $# -gt 0 ]; do
        case "$1" in
            -o) out="$2"; shift 2 ;;
            -H) hdr="$hdr|$2"; shift 2 ;;
            --retry|--retry-delay|--max-time|--max-filesize|--connect-timeout|--speed-limit|--speed-time) shift 2 ;;
            -*) shift ;;
            *) url="$1"; shift ;;
        esac
    done
    printf '%s %s\n' "$url" "$hdr" >> "$CURL_LOG"
    case "$url" in
        https://api.github.com/*/releases/tags/*)
            [ "$hdr" = "|Accept: application/vnd.github+json" ] || return 22
            [ -n "$TAGS" ] || return 22
            printf '%s' "$TAGS" > "$out" ;;
        https://api.github.com/*/releases/assets/*)
            [ "$hdr" = "|Accept: application/octet-stream" ] || return 22
            printf 'asset %s' "${url##*/}" > "$out" ;;
        *) [ "$LINK" = ok ] || return 22; printf 'link' > "$out" ;;
    esac
}
`

func TestGhFetch(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash here")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 here")
	}
	const src = "https://github.com/alonsovidales/otc/releases/download/v177/src.tar.gz"
	release := `{"tag_name":"v177","assets":[` +
		`{"id":725529777,"name":"web-dist.tar.gz","uploader":{"id":1,"login":"x"}},` +
		`{"id":725529776,"name":"src.tar.gz","uploader":{"id":2,"login":"y"}}]}`
	cases := []struct {
		name, url, link, tags string
		want                  string // the file's content, "" = gh_fetch fails
		calls                 []string
	}{
		{"the link works", src, "ok", release, "link", []string{src + " "}},
		{"the link fails, the API serves it", src, "", release, "asset 725529776", []string{
			src + " ",
			"https://api.github.com/repos/alonsovidales/otc/releases/tags/v177 |Accept: application/vnd.github+json",
			"https://api.github.com/repos/alonsovidales/otc/releases/assets/725529776 |Accept: application/octet-stream",
		}},
		{"a pinned tool from another repository", "https://github.com/protocolbuffers/protobuf/releases/download/v29.3/protoc-29.3-linux-aarch_64.zip", "",
			`{"assets":[{"id":7,"name":"protoc-29.3-linux-aarch_64.zip"}]}`, "asset 7", []string{
				"https://github.com/protocolbuffers/protobuf/releases/download/v29.3/protoc-29.3-linux-aarch_64.zip ",
				"https://api.github.com/repos/protocolbuffers/protobuf/releases/tags/v29.3 |Accept: application/vnd.github+json",
				"https://api.github.com/repos/protocolbuffers/protobuf/releases/assets/7 |Accept: application/octet-stream",
			}},
		{"not on github.com (a fork's own host)", "https://git.example.org/me/otc/releases/download/v177/src.tar.gz", "", release, "",
			[]string{"https://git.example.org/me/otc/releases/download/v177/src.tar.gz "}},
		{"not a release link", "https://github.com/opencv/opencv/archive/refs/tags/4.10.0.tar.gz", "", release, "",
			[]string{"https://github.com/opencv/opencv/archive/refs/tags/4.10.0.tar.gz "}},
		{"a query after the name", src + "?x=1", "", release, "", []string{src + "?x=1 "}},
		{"the release has no such file", "https://github.com/alonsovidales/otc/releases/download/v177/other.tar.gz", "", release, "", []string{
			"https://github.com/alonsovidales/otc/releases/download/v177/other.tar.gz ",
			"https://api.github.com/repos/alonsovidales/otc/releases/tags/v177 |Accept: application/vnd.github+json",
		}},
		{"the API fails too", src, "", "", "", []string{
			src + " ",
			"https://api.github.com/repos/alonsovidales/otc/releases/tags/v177 |Accept: application/vnd.github+json",
		}},
		{"the API answers something else", src, "", `{"message":"Not Found"}`, "", []string{
			src + " ",
			"https://api.github.com/repos/alonsovidales/otc/releases/tags/v177 |Accept: application/vnd.github+json",
		}},
		{"an id that isn't a number", src, "", `{"assets":[{"id":"1; touch /tmp/x","name":"src.tar.gz"}]}`, "", []string{
			src + " ",
			"https://api.github.com/repos/alonsovidales/otc/releases/tags/v177 |Accept: application/vnd.github+json",
		}},
		{"an id in other digits", src, "", `{"assets":[{"id":"٤٢","name":"src.tar.gz"}]}`, "", []string{
			src + " ",
			"https://api.github.com/repos/alonsovidales/otc/releases/tags/v177 |Accept: application/vnd.github+json",
		}},
		{"an id that is a boolean", src, "", `{"assets":[{"id":true,"name":"src.tar.gz"}]}`, "", []string{
			src + " ",
			"https://api.github.com/repos/alonsovidales/otc/releases/tags/v177 |Accept: application/vnd.github+json",
		}},
		{"JSON that isn't", src, "", `<html>504</html>`, "", []string{
			src + " ",
			"https://api.github.com/repos/alonsovidales/otc/releases/tags/v177 |Accept: application/vnd.github+json",
		}},
	}
	for _, script := range ghFetchScripts[:3] {
		fns := shellFunc(t, script, "gh_api_fetch") + shellFunc(t, script, "gh_fetch")
		for _, c := range cases {
			dir := t.TempDir()
			out := filepath.Join(dir, "out")
			log := filepath.Join(dir, "curl.log")
			// As the scripts run it: verified-install.sh under set -e, the
			// call in a || list.
			body := "set -euo pipefail\nCURL_STALL=(--connect-timeout 30)\n" + fakeCurl + fns +
				`gh_fetch "$1" "$2" || { echo failed; exit 3; }; echo fetched`
			cmd := exec.Command("bash", "-c", body, "bash", c.url, out)
			cmd.Env = append(os.Environ(), "CURL_LOG="+log, "LINK="+c.link, "TAGS="+c.tags, "LC_ALL=en_US.UTF-8")
			got, err := cmd.CombinedOutput()
			if c.want == "" {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 || !strings.Contains(string(got), "failed") {
					t.Errorf("%s, %s: %v: %s", script, c.name, err, got)
				}
			} else {
				if err != nil || !strings.HasSuffix(strings.TrimSpace(string(got)), "fetched") {
					t.Errorf("%s, %s: %v: %s", script, c.name, err, got)
				}
				if b, _ := os.ReadFile(out); string(b) != c.want {
					t.Errorf("%s, %s: fetched %q, want %q", script, c.name, b, c.want)
				}
			}
			calls, _ := os.ReadFile(log)
			if want := strings.Join(c.calls, "\n") + "\n"; string(calls) != want {
				t.Errorf("%s, %s: curl was asked\n%s\nwant\n%s", script, c.name, calls, want)
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "*.release")); len(left) != 0 {
				t.Errorf("%s, %s: left behind %v", script, c.name, left)
			}
		}
	}
}

// install.sh's fetch_pinned and fetch_optional take any URL: their own curl
// first, then, for a GitHub release file, gh_api_fetch; the hash checked
// either way.
func TestInstallFetchesFallBackToTheAPI(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash here")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 here")
	}
	const url = "https://github.com/microsoft/onnxruntime/releases/download/v1.24.3/onnxruntime-linux-aarch64-1.24.3.tgz"
	const tags = `{"assets":[{"id":9,"name":"onnxruntime-linux-aarch64-1.24.3.tgz"}]}`
	sha := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	fns := shellFunc(t, "../scripts/install.sh", "gh_api_fetch") +
		shellFunc(t, "../scripts/install.sh", "fetch_pinned") + shellFunc(t, "../scripts/install.sh", "fetch_optional")
	// GNU mv's -T, and stand-ins for install.sh's own helpers.
	stubs := `mv() { if [ "$1" = -Tf ]; then command mv -f "$2" "$3"; else command mv "$@"; fi; }
log() { echo "log: $*"; }
die() { echo "die: $*"; exit 4; }
wiz_error() { :; }
command -v sha256sum >/dev/null || sha256sum() { shasum -a 256 "$@"; }
`
	for _, c := range []struct {
		name, fn, link, tags, want, content string // content "" = nothing stored
		apiCalls                            int
	}{
		{"pinned, the link works", "fetch_pinned", "ok", tags, sha("link"), "link", 0},
		{"pinned, through the API", "fetch_pinned", "", tags, sha("asset 9"), "asset 9", 2},
		{"pinned, neither", "fetch_pinned", "", "", sha("asset 9"), "", 1},
		{"pinned, the API's file isn't the pin", "fetch_pinned", "", tags, sha("other"), "", 2},
		{"optional, the link works", "fetch_optional", "ok", tags, sha("link"), "link", 0},
		{"optional, through the API", "fetch_optional", "", tags, sha("asset 9"), "asset 9", 2},
		{"optional, neither", "fetch_optional", "", "", sha("asset 9"), "", 1},
	} {
		dir := t.TempDir()
		dest := filepath.Join(dir, "ort.tgz")
		log := filepath.Join(dir, "curl.log")
		body := "set -e\n" + stubs + fakeCurl + fns + c.fn + ` "$1" "$2" "$3"; echo returned`
		cmd := exec.Command("bash", "-c", body, "bash", url, dest, c.want)
		cmd.Env = append(os.Environ(), "CURL_LOG="+log, "LINK="+c.link, "TAGS="+c.tags)
		out, err := cmd.CombinedOutput()
		b, readErr := os.ReadFile(dest)
		switch {
		case c.content != "":
			if err != nil || readErr != nil || string(b) != c.content {
				t.Errorf("%s: %v, stored %q (%v): %s", c.name, err, b, readErr, out)
			}
		case c.fn == "fetch_pinned":
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 4 || readErr == nil {
				t.Errorf("%s: want die and nothing stored, got %v, %q: %s", c.name, err, b, out)
			}
		default:
			if err != nil || readErr == nil || !strings.Contains(string(out), "log: warning") {
				t.Errorf("%s: want a warning and nothing stored, got %v, %q: %s", c.name, err, b, out)
			}
		}
		calls, _ := os.ReadFile(log)
		if n := strings.Count(string(calls), "https://api.github.com/"); n != c.apiCalls {
			t.Errorf("%s: %d calls to the API, want %d:\n%s", c.name, n, c.apiCalls, calls)
		}
		if left, _ := filepath.Glob(filepath.Join(dir, ".otc-fetch.*")); len(left) != 0 {
			t.Errorf("%s: left behind %v", c.name, left)
		}
	}
}
