// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// manifestScripts reads scripts/updates/VERSIONS: each release's script
// hash, "-" for a release with none.
func manifestScripts(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("../scripts/updates/VERSIONS")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scripts := map[string]string{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		line := s.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		scripts[fields[0]] = fields[1]
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return scripts
}

// A released script is never edited: every device checks it against the
// hash its release signed into the manifest before it runs it as root, so
// an edited one fails every update that reaches it. One that turns out
// wrong is corrected in scripts/updates/corrected/<N>.sh instead, which
// update.sh runs in its place - and only for a release that has a script
// of its own (a corrected copy of nothing would never run).
func TestReleasedScriptsMatchTheManifest(t *testing.T) {
	scripts := manifestScripts(t)
	if len(scripts) < 100 {
		t.Fatalf("only %d releases read from VERSIONS: has its format changed under this test?", len(scripts))
	}
	files, err := filepath.Glob("../scripts/updates/*.sh")
	if err != nil {
		t.Fatal(err)
	}
	isNumber := regexp.MustCompile(`^[0-9]+$`)
	checked := 0
	for _, path := range files {
		version := strings.TrimSuffix(filepath.Base(path), ".sh")
		if !isNumber.MatchString(version) {
			t.Errorf("%s: release scripts are named <release>.sh", path)
			continue
		}
		want, released := scripts[version]
		if !released {
			continue // the next release's script, not signed yet
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("scripts/updates/%s.sh was changed after its release: its hash is %s, the signed manifest says %s - put the change in scripts/updates/corrected/%s.sh", version, got, want, version)
		}
		checked++
	}
	if checked < 40 {
		t.Errorf("only %d released scripts checked", checked)
	}
	for version, sha := range scripts {
		if sha == "-" {
			continue
		}
		if _, err := os.Stat("../scripts/updates/" + version + ".sh"); err != nil {
			t.Errorf("release %s's script is in the manifest but not in scripts/updates: %v", version, err)
		}
	}

	corrected, err := filepath.Glob("../scripts/updates/corrected/*.sh")
	if err != nil {
		t.Fatal(err)
	}
	if len(corrected) == 0 {
		t.Fatal("no corrected scripts found")
	}
	for _, path := range corrected {
		version := strings.TrimSuffix(filepath.Base(path), ".sh")
		if sha, ok := scripts[version]; !ok || sha == "-" {
			t.Errorf("%s corrects release %s, which has no released script", path, version)
		}
	}
}
