// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

func TestWindowsUnusableNames(t *testing.T) {
	for rel, want := range map[string]bool{
		`a\b.txt`: true, "report.": true, "report ": true, "dir./a.txt": true, "a?.txt": true, `say "hi".txt`: true,
		"x|y": true, "a<b>": true, "star*.png": true, "aux.txt": true, "Docs/CON": true, "nul": true, "COM1.log": true,
		"lpt9": true, "tab\there": true,
		"report.txt": false, "a.b.c": false, ".hidden": false, "console.txt": false, "com10.txt": false, "Photos/2024/a.jpg": false,
		"été.txt": false,
	} {
		if got := windowsUnusable(rel); got != want {
			t.Errorf("%q: %v, want %v", rel, got, want)
		}
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
