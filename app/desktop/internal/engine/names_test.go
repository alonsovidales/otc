// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

func TestWindowsUnusableNames(t *testing.T) {
	for rel, want := range map[string]bool{
		`a\b.txt`: true, "report.": true, "report ": true, "dir./a.txt": true, "a?.txt": true, `say "hi".txt`: true,
		"x|y": true, "a<b>": true, "star*.png": true, "Docs/CON": true, "nul": true, "Aux/a.txt": true,
		"lpt9": true, "COM¹": true, "conin$": true, "tab\there": true, "con.": true, "nul ": true,
		"report.txt": false, "a.b.c": false, ".hidden": false, "console.txt": false, "com10.txt": false, "Photos/2024/a.jpg": false,
		"été.txt": false, "CONıN$": false,
	} {
		if got := windowsUnusable(rel); got != want {
			t.Errorf("%q: %v, want %v", rel, got, want)
		}
	}
	// A device name with an extension is one only where this Windows says
	// so (Windows 11 holds "con.txt"); never elsewhere.
	for _, rel := range []string{"aux.txt", "con.jpg", "COM1.log", "nul.tar.gz", "Docs/prn.pdf"} {
		want := runtime.GOOS == "windows" && !filepath.IsLocal(filepath.FromSlash(rel))
		if got := windowsUnusable(rel); got != want {
			t.Errorf("%q: %v, want %v", rel, got, want)
		}
	}
}

// A device file left out for its name has its path left out here too: a
// file here at it isn't taken for deleted on the device (moved to the
// trash) nor sent over the device's. Seen after the update, where the
// first passes had synced "report." and "aux".
func TestFilesHereAtExcludedNamesAreLeftAlone(t *testing.T) {
	withConfigDir(t)
	old := windowsNames
	t.Cleanup(func() { windowsNames = old })
	windowsNames = false
	d := storedDevice(map[string][]byte{"/pc/docs/report.": []byte("2"), "/pc/docs/aux": []byte("device's"), "/pc/docs/ok.txt": []byte("3")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	if n := countFiles(t, local); n != 3 {
		t.Fatalf("first pass: %d files here", n)
	}
	windowsNames = true
	// A file here at an excluded name, changed: not sent over the device's.
	if err := os.WriteFile(filepath.Join(local, "aux"), []byte("this computer's"), 0o644); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		e.reconcileRemoteFolder(f)
	}
	got, deletes, _ := d.snapshot()
	if len(deletes) != 0 || len(got) != 3 || string(got["/pc/docs/aux"]) != "device's" {
		t.Fatalf("deletes %v, device %v, aux there %q", deletes, keysOf(got), got["/pc/docs/aux"])
	}
	for name, want := range map[string]string{"report.": "2", "aux": "this computer's", "ok.txt": "3"} {
		if b, err := os.ReadFile(filepath.Join(local, name)); err != nil || string(b) != want {
			t.Fatalf("%s here: %q %v", name, b, err)
		}
	}
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.names_not_allowed" {
		t.Fatalf("state %+v", st)
	}
	// And a new file here at a name the device has but this computer
	// can't hold isn't sent over it.
	windowsNames = false
	d2 := storedDevice(map[string][]byte{"/pc/new/nul": []byte("device's")})
	local2 := t.TempDir()
	writeFiles(t, local2, map[string]string{"nul": "this computer's"})
	windowsNames = true
	f2 := config.RemoteFolder{ID: "r2", RemotePath: "/pc/new", LocalPath: local2}
	e2 := twoWayEngine(t, d2, "dev", f2)
	for pass := 0; pass < 2; pass++ {
		e2.reconcileRemoteFolder(f2)
	}
	got2, deletes2, _ := d2.snapshot()
	if len(deletes2) != 0 || string(got2["/pc/new/nul"]) != "device's" || countFiles(t, local2) != 1 {
		t.Fatalf("deletes %v, nul there %q, %d here", deletes2, got2["/pc/new/nul"], countFiles(t, local2))
	}
}

// On Windows a device file whose name Windows can't hold is left alone -
// not downloaded under another name, so never moved on the device - and
// counted on the folder.
func TestNamesWindowsCantHoldAreLeftAlone(t *testing.T) {
	withConfigDir(t)
	old := windowsNames
	windowsNames = true
	t.Cleanup(func() { windowsNames = old })
	d := storedDevice(map[string][]byte{`/pc/docs/a\b.txt`: []byte("1"), "/pc/docs/report.": []byte("2"), "/pc/docs/ok.txt": []byte("3")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	for pass := 0; pass < 2; pass++ {
		e.reconcileRemoteFolder(f)
	}
	got, deletes, _ := d.snapshot()
	if len(deletes) != 0 || len(got) != 3 || countFiles(t, local) != 1 {
		t.Fatalf("deletes %v, device %v, %d here", deletes, keysOf(got), countFiles(t, local))
	}
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.names_not_allowed" {
		t.Fatalf("state %+v", st)
	}
}
