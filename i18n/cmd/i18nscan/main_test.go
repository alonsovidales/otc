// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n/scan"
)

const testGo = `package websocket

import "errors"

var errA = errors.New("no such job")
`

// writeRepo builds a repository from path -> content.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scanRun runs the command and returns its exit code, stdout and stderr.
func scanRun(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append([]string{"-root", root}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestRatchet(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"websocket/a.go": testGo,
		"websocket/b.go": "package websocket\n\nvar errB = errorsNew(\"left alone\")\n",
	})
	dev := []string{"-surface", "device"}

	// no baseline yet: the check fails and says how to write one
	code, _, errs := scanRun(t, root, dev...)
	if code != 1 || !strings.Contains(errs, "has no baseline") {
		t.Fatalf("no baseline: code %d, stderr %q", code, errs)
	}
	// -update writes it
	code, out, errs := scanRun(t, root, append(dev, "-update")...)
	if code != 0 || !strings.Contains(out, "wrote i18n/scan/baseline/device.json") {
		t.Fatalf("first update: code %d, out %q, err %q", code, out, errs)
	}
	data, err := os.ReadFile(filepath.Join(root, "i18n", "scan", "baseline", "device.json"))
	if err != nil || !strings.Contains(string(data), `"websocket/a.go": 1`) {
		t.Fatalf("baseline %q, %v", data, err)
	}
	if code, _, errs := scanRun(t, root, dev...); code != 0 {
		t.Fatalf("clean check: code %d, %q", code, errs)
	}

	// a new text: the check fails and lists the file's findings
	writeFile(t, root, "websocket/a.go", testGo+"var errC = errors.New(\"the device is busy\")\n")
	code, _, errs = scanRun(t, root, dev...)
	if code != 1 || !strings.Contains(errs, "websocket/a.go: 2 hard-coded texts, baseline 1 (+1)") ||
		!strings.Contains(errs, "websocket/a.go:6: prose: the device is busy") {
		t.Fatalf("rise: code %d, stderr %q", code, errs)
	}
	// -path narrows the check: b.go didn't change
	if code, _, _ := scanRun(t, root, append(dev, "-path", "websocket/b.go")...); code != 0 {
		t.Errorf("-path b.go: code %d", code)
	}
	// -update refuses to raise a count, and writes nothing
	code, _, errs = scanRun(t, root, append(dev, "-update")...)
	if code != 1 || !strings.Contains(errs, "refusing to raise it") {
		t.Fatalf("raise refused: code %d, stderr %q", code, errs)
	}
	if again, _ := os.ReadFile(filepath.Join(root, "i18n", "scan", "baseline", "device.json")); !bytes.Equal(again, data) {
		t.Error("a refused update changed the baseline")
	}
	// an ignored line doesn't count
	writeFile(t, root, "websocket/a.go", testGo+"var errC = errors.New(\"the device is busy\") // i18n-ignore: test\n")
	if code, out, _ := scanRun(t, root, dev...); code != 0 || !strings.Contains(out, "1 ignored") {
		t.Errorf("ignored: code %d, out %q", code, out)
	}
	// -allow-increase writes it
	writeFile(t, root, "websocket/a.go", testGo+"var errC = errors.New(\"the device is busy\")\n")
	if code, _, errs := scanRun(t, root, append(dev, "-update", "-allow-increase")...); code != 0 {
		t.Fatalf("allow increase: code %d, %q", code, errs)
	}

	// texts moved out: the check passes and suggests locking it in
	writeFile(t, root, "websocket/a.go", "package websocket\n")
	code, out, _ = scanRun(t, root, dev...)
	if code != 0 || !strings.Contains(out, "1 file is below the baseline") {
		t.Fatalf("fall: code %d, out %q", code, out)
	}
	if code, _, _ := scanRun(t, root, append(dev, "-update")...); code != 0 {
		t.Fatalf("lowering update: code %d", code)
	}
	data, _ = os.ReadFile(filepath.Join(root, "i18n", "scan", "baseline", "device.json"))
	if strings.Contains(string(data), "websocket/a.go") {
		t.Errorf("a file at zero stays in the baseline: %s", data)
	}
	// nothing changed: -update writes nothing
	if _, out, _ := scanRun(t, root, append(dev, "-update")...); strings.Contains(out, "wrote") {
		t.Errorf("an unchanged baseline was rewritten: %q", out)
	}
}

func TestFirstUpdateCoversTheSurface(t *testing.T) {
	root := writeRepo(t, map[string]string{"websocket/a.go": testGo, "social/b.go": strings.Replace(testGo, "websocket", "social", 1)})
	if code, _, errs := scanRun(t, root, "-surface", "device", "-update", "-path", "social"); code != 0 {
		t.Fatalf("code %d, %q", code, errs)
	}
	data, _ := os.ReadFile(filepath.Join(root, "i18n", "scan", "baseline", "device.json"))
	if !strings.Contains(string(data), "websocket/a.go") || !strings.Contains(string(data), "social/b.go") {
		t.Errorf("first baseline %s", data)
	}
}

func TestVerbose(t *testing.T) {
	root := writeRepo(t, map[string]string{"websocket/a.go": testGo})
	_, out, _ := scanRun(t, root, "-surface", "device", "-v")
	if !strings.Contains(out, "    1 websocket/a.go") || !strings.Contains(out, "websocket/a.go:5: prose: no such job") {
		t.Errorf("-v output %q", out)
	}
}

func TestUsage(t *testing.T) {
	root := writeRepo(t, map[string]string{"websocket/a.go": testGo})
	for _, args := range [][]string{
		{"-surface", "nope"},
		{"-allow-increase"},
		{"extra"},
		{"-nosuchflag"},
	} {
		if code, _, _ := scanRun(t, root, args...); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
}

func TestScanErrorStopsUpdate(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"websocket/a.go": testGo,
		"web/src/a.tsx":  "export const a = <p>Hello there</p>;\n",
	})
	// no web/node_modules here: the web surface can't be scanned
	code, _, errs := scanRun(t, root, "-surface", "web,device", "-update", "-typescript", filepath.Join(root, "missing"))
	if code != 2 || !strings.Contains(errs, "nothing written") {
		t.Fatalf("code %d, stderr %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(root, "i18n", "scan", "baseline", "device.json")); err == nil {
		t.Error("a baseline was written although a surface failed")
	}
}

// TestCommittedBaselines: every committed baseline parses, names a known
// surface and is in canonical form (what -update writes). Whether the
// counts hold is the job of the check itself (make i18n-check), not of go
// test: other work in progress would make this package's tests flaky.
func TestCommittedBaselines(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(scan.BaselineDir)))
	if err != nil {
		t.Skip("no baseline committed")
	}
	known := map[string]bool{}
	for _, n := range scan.SurfaceNames() {
		known[n] = true
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasPrefix(name, ".") {
			continue
		}
		if !known[name] {
			t.Errorf("%s: no surface %q", e.Name(), name)
			continue
		}
		b, _, err := scan.LoadBaseline(root, name)
		if err != nil {
			t.Error(err)
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(scan.BaselineFile(name))))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, b.Encode()) {
			t.Errorf("%s is not in canonical form (run go run ./i18n/cmd/i18nscan -update -surface %s)", scan.BaselineFile(name), name)
		}
	}
}
