// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
)

// storedDevice is a fake device whose listings are what it stores now.
func storedDevice(files map[string][]byte) *fakeDevice {
	d := &fakeDevice{files: map[string][]byte{}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}, listStored: true}
	for p, b := range files {
		d.files[p] = b
	}

	return d
}

// twoWayEngine is an engine connected to d with the one two-way folder f,
// for the device at domain.
func twoWayEngine(t *testing.T, d *fakeDevice, domain string, f config.RemoteFolder) *Engine {
	t.Helper()
	conn := connectedEngine(t, d)
	e := New(&config.Config{Domain: domain, RemoteFolders: []config.RemoteFolder{f}}, "pw", nil)
	e.ws = conn.ws

	return e
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}

		return nil
	})

	return n
}

func (d *fakeDevice) snapshot() (files map[string][]byte, deletes []string, reads int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	files = map[string][]byte{}
	for k, v := range d.files {
		files[k] = v
	}

	return files, append([]string(nil), d.deletes...), d.reads
}

// A sync record made with another device is no record at all. Pointed
// at another device (the address changed without Disconnect), the first
// pass used to take every file the new device lacks for one deleted there
// and delete it here - all ten, under the mass-deletion guard's threshold.
func TestRecordOfAnotherDeviceDeletesNothing(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(nil)
	local := t.TempDir()
	rec := map[string]string{}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("doc%d.txt", i)
		writeFiles(t, local, map[string]string{name: "content " + name})
		rec[name] = sha([]byte("content " + name))
	}
	f := config.RemoteFolder{ID: "r1", RemotePath: "/linux/pc/home/u/Docs", LocalPath: local}
	e := twoWayEngine(t, d, "b.off-the.cloud", f)
	e.saveSyncedFor(f.ID, recordDevice{Domain: "a.off-the.cloud"}, rec) // device A's

	e.reconcileRemoteFolder(f)
	files, deletes, _ := d.snapshot()
	if n := countFiles(t, local); n != 10 || len(files) != 10 || len(deletes) != 0 {
		t.Fatalf("%d of 10 files left here, %d on the new device, %d deletes sent", n, len(files), len(deletes))
	}
	// The record is the new device's now: a delete made here goes there.
	if err := os.Remove(filepath.Join(local, "doc0.txt")); err != nil {
		t.Fatal(err)
	}
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 1 || !strings.HasSuffix(deletes[0], "/doc0.txt") {
		t.Fatalf("deletes after a delete here: %v", deletes)
	}
}

// An older version's record (the bare map) is the configured device's, as
// it always was: deletes made here still go.
func TestOlderRecordIsTheConfiguredDevices(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/folder/a.txt": []byte("a")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
	e := twoWayEngine(t, d, "cala.off-the.cloud", f)
	p, _ := syncedPath(f.ID)
	if err := os.WriteFile(p, []byte(`{"a.txt": "`+sha([]byte("a"))+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 1 {
		t.Fatalf("deletes: %v", deletes)
	}
	data, _ := os.ReadFile(p)
	if _, device, ok := parseRecord(data); !ok || device == nil || device.Domain != "cala.off-the.cloud" {
		t.Fatalf("record rewritten as %s", data)
	}
}

// The device's pin names it whatever its address: a record follows the
// device to another address, and a device set up again from scratch under
// the same name (a new pin) gets no record.
func TestRecordGoesByTheDevicePin(t *testing.T) {
	pin := bytes.Repeat([]byte{7}, 32)
	for _, tc := range []struct {
		name       string
		recordPin  []byte
		wantDelete bool
	}{
		{"same device, another address", pin, true},
		{"another device", bytes.Repeat([]byte{9}, 32), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withConfigDir(t)
			d := storedDevice(map[string][]byte{"/pc/folder/a.txt": []byte("a")})
			local := t.TempDir()
			f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
			e := twoWayEngine(t, d, "ws://192.168.1.20:8080/ws", f)
			e.ws.SetLocalEndpoint(wsclient.NewLocalEndpoint([]string{"192.168.1.20"}, 8443, pin))
			e.saveSyncedFor(f.ID, recordDevice{Domain: "cala.off-the.cloud", Pin: hex.EncodeToString(tc.recordPin)},
				map[string]string{"a.txt": sha([]byte("a"))})
			e.reconcileRemoteFolder(f)
			_, deletes, _ := d.snapshot()
			if got := len(deletes) == 1; got != tc.wantDelete {
				t.Fatalf("delete sent %v, want %v", got, tc.wantDelete)
			}
			if !tc.wantDelete && countFiles(t, local) != 1 {
				t.Fatal("the device's file did not come down")
			}
		})
	}
}

func TestRecordDeviceMatches(t *testing.T) {
	a := recordDevice{Domain: "cala.off-the.cloud", Pin: "aa"}
	for _, tc := range []struct {
		b    recordDevice
		want bool
	}{
		{recordDevice{Domain: "ws://192.168.1.2:8080/ws", Pin: "aa"}, true},
		{recordDevice{Domain: "cala.off-the.cloud", Pin: "bb"}, false},
		{recordDevice{Domain: "cala.off-the.cloud"}, true},
		{recordDevice{Domain: "other.off-the.cloud"}, false},
	} {
		if got := a.matches(tc.b); got != tc.want {
			t.Errorf("%+v matches %+v: %v, want %v", a, tc.b, got, tc.want)
		}
	}
	for _, same := range [][2]string{
		{"cala.off-the.cloud", "Cala.off-the.cloud"},
		{"cala.off-the.cloud", "wss://cala.off-the.cloud/ws"},
		{"ws://192.168.1.2:8080/ws", "WS://192.168.1.2:8080/ws/"},
	} {
		if !SameDevice(same[0], same[1]) {
			t.Errorf("%q and %q are the same device", same[0], same[1])
		}
	}
	if SameDevice("cala.off-the.cloud", "pit.off-the.cloud") {
		t.Error("two names, one device")
	}
}

func TestParseRecord(t *testing.T) {
	if files, dev, ok := parseRecord([]byte(`{"v": 2, "device": {"domain": "d"}, "files": {"a": "h"}}`)); !ok || dev == nil || dev.Domain != "d" || files["a"] != "h" {
		t.Errorf("this version's: %v %v %v", files, dev, ok)
	}
	// An older one, even with files named v and device.
	if files, dev, ok := parseRecord([]byte(`{"v": "h1", "device": "h2"}`)); !ok || dev != nil || files["v"] != "h1" || files["device"] != "h2" {
		t.Errorf("older: %v %v %v", files, dev, ok)
	}
	if _, _, ok := parseRecord([]byte(`not json`)); ok {
		t.Error("garbage read as a record")
	}
	// Keys in NFC; of two spellings the NFC one wins.
	files, _, _ := parseRecord([]byte(`{"Résumé.pdf": "nfd", "Résumé.pdf": "nfc"}`))
	if len(files) != 1 || files["Résumé.pdf"] != "nfc" {
		t.Errorf("normalized keys: %v", files)
	}
}

// The listing a long pass acts on can be old. A file edited on a phone
// after it was taken is not overwritten (nor deleted): the next pass sees
// both sides changed and keeps both versions.
func TestDeviceEditDuringThePassIsNotOverwritten(t *testing.T) {
	withConfigDir(t)
	v1 := []byte("version 1")
	d := storedDevice(map[string][]byte{"/pc/folder/a.txt": v1})
	local := t.TempDir()
	mine := "version 1 + an edit made on this computer"
	writeFiles(t, local, map[string]string{"a.txt": mine})
	phone := []byte("version 2, made on a phone during the pass")
	d.afterList = func() { d.files["/pc/folder/a.txt"] = phone }
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.saveSynced(f.ID, map[string]string{"a.txt": sha(v1)})

	e.reconcileRemoteFolder(f)
	files, _, _ := d.snapshot()
	if !bytes.Equal(files["/pc/folder/a.txt"], phone) {
		t.Fatalf("the phone's version was replaced by %q", files["/pc/folder/a.txt"])
	}
	e.reconcileRemoteFolder(f)
	files, _, _ = d.snapshot()
	entries, _ := os.ReadDir(local)
	var copies []string
	for _, en := range entries {
		if strings.Contains(en.Name(), "(conflict") {
			copies = append(copies, en.Name())
			if got, _ := os.ReadFile(filepath.Join(local, en.Name())); !bytes.Equal(got, phone) {
				t.Errorf("the conflict copy holds %q", got)
			}
		}
	}
	if len(copies) != 1 || string(files["/pc/folder/a.txt"]) != mine {
		t.Fatalf("after the next pass: copies %v, device has %q", copies, files["/pc/folder/a.txt"])
	}
}

// A delete made here doesn't remove a version the device got after
// the listing; it comes down instead.
func TestDeleteSkippedWhenTheDeviceFileChanged(t *testing.T) {
	withConfigDir(t)
	v1 := []byte("version 1")
	d := storedDevice(map[string][]byte{"/pc/folder/a.txt": v1})
	local := t.TempDir() // a.txt was deleted here
	phone := []byte("edited on a phone")
	d.afterList = func() { d.files["/pc/folder/a.txt"] = phone }
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.saveSynced(f.ID, map[string]string{"a.txt": sha(v1)})

	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 0 {
		t.Fatalf("deleted on the device: %v", deletes)
	}
	e.reconcileRemoteFolder(f)
	if got, _ := os.ReadFile(filepath.Join(local, "a.txt")); !bytes.Equal(got, phone) {
		t.Fatalf("a.txt here is %q, want the phone's version", got)
	}
}

// A folder renamed or moved on the device is no mass deletion: its
// files are the new names' content. The guard used to send the 30 old
// names back next to the new ones.
func TestRenameOnTheDeviceIsNotAMassDeletion(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(nil)
	local := t.TempDir()
	rec := map[string]string{}
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("p%02d.txt", i)
		content := "project " + name
		writeFiles(t, local, map[string]string{"Projects/" + name: content})
		rec["Projects/"+name] = sha([]byte(content))
		d.files["/pc/folder/Archive/"+name] = []byte(content) // renamed there
	}
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.saveSynced(f.ID, rec)

	e.reconcileRemoteFolder(f)
	files, _, _ := d.snapshot()
	old := 0
	for p := range files {
		if strings.HasPrefix(p, "/pc/folder/Projects/") {
			old++
		}
	}
	archived, _ := os.ReadDir(filepath.Join(local, "Archive"))
	if _, err := os.Stat(filepath.Join(local, "Projects")); !os.IsNotExist(err) || old != 0 || len(archived) != 30 {
		t.Fatalf("old names back on the device: %d; Projects here: %v; Archive here: %d", old, err, len(archived))
	}
	if st := e.remoteStates[f.ID]; st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
}

// A two-way folder whose folder isn't there (a disk unplugged) waits:
// it stays configured with its record, says so, and carries on once it is
// back. It used to be dropped from config.json, record and all.
func TestMissingFolderWaitsAndComesBack(t *testing.T) {
	withConfigDir(t)
	photo := []byte("photo")
	d := storedDevice(map[string][]byte{"/Photos/a.jpg": photo})
	missing := filepath.Join(t.TempDir(), "usb-drive", "Photos")
	f := config.RemoteFolder{ID: "r1", RemotePath: "/Photos", LocalPath: missing}
	cfg := &config.Config{Domain: "dev", RemoteFolders: []config.RemoteFolder{f}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	conn := connectedEngine(t, d)
	e := New(cfg, "pw", nil)
	e.ws = conn.ws
	t.Cleanup(e.Stop)
	e.saveSynced(f.ID, map[string]string{"a.jpg": sha(photo)})

	e.reconcileRemoteFolder(f)
	onDisk, err := config.Load()
	if err != nil || len(onDisk.RemoteFolders) != 1 {
		t.Fatalf("config.json: %+v %v", onDisk, err)
	}
	p, _ := syncedPath(f.ID)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("record: %v", err)
	}
	e.mu.Lock()
	st := e.remoteStates[f.ID]
	e.mu.Unlock()
	if st.Kind != StateError || st.Msg == nil || st.Msg.Key != "desk.main.folder_not_found" {
		t.Fatalf("state %+v", st)
	}
	if _, deletes, reads := d.snapshot(); len(deletes) != 0 || reads != 0 {
		t.Fatalf("acted while missing: %v deletes, %d reads", deletes, reads)
	}

	// The drive is back, as it was: in sync, nothing fetched again.
	writeFiles(t, missing, map[string]string{"a.jpg": "photo"})
	e.reconcileRemoteFolder(f)
	e.mu.Lock()
	st, watched := e.remoteStates[f.ID], e.remoteWatch[f.ID] != nil
	e.mu.Unlock()
	if _, _, reads := d.snapshot(); st.Kind != StateWatching || reads != 0 || !watched {
		t.Fatalf("back: state %+v, %d reads, watched %v", st, reads, watched)
	}
}

// The same name in two Unicode normalizations is one file. The device
// keeps the bytes it was sent (the Mac's are NFD, a phone's NFC); a file
// it has is updated at its own path, a new one goes up in NFC, and a name
// it lists twice is never deleted or renamed for it.
func TestNormalizationKeysAndDevicePaths(t *testing.T) {
	withConfigDir(t)
	nfd, nfc := "Résumé.pdf", "Résumé.pdf"
	d := storedDevice(map[string][]byte{"/pc/folder/" + nfd: []byte("cv")})
	local := t.TempDir()
	writeFiles(t, local, map[string]string{nfc: "cv"})
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)

	e.reconcileRemoteFolder(f)
	if _, _, reads := d.snapshot(); reads != 0 || len(d.pending) != 0 {
		t.Fatalf("one file seen as two: %d reads, %d uploads", reads, len(d.pending))
	}
	if e.lastSynced[f.ID][nfc] != sha([]byte("cv")) {
		t.Fatalf("record %v", e.lastSynced[f.ID])
	}
	// An edit here goes to the device's own path.
	writeFiles(t, local, map[string]string{nfc: "cv, updated", "Café.txt": "new"})
	e.reconcileRemoteFolder(f)
	files, deletes, _ := d.snapshot()
	if string(files["/pc/folder/"+nfd]) != "cv, updated" || files["/pc/folder/"+nfc] != nil {
		t.Fatalf("device after an edit: %q", keysOf(files))
	}
	// A new file goes up in NFC.
	if files["/pc/folder/Café.txt"] == nil || len(deletes) != 0 {
		t.Fatalf("device after a new file: %q, deletes %v", keysOf(files), deletes)
	}

	// Twins on the device: one comes down, neither is deleted.
	d2 := storedDevice(map[string][]byte{"/pc/f2/" + nfd: []byte("nfd"), "/pc/f2/" + nfc: []byte("nfc")})
	local2 := t.TempDir()
	f2 := config.RemoteFolder{ID: "r2", RemotePath: "/pc/f2", LocalPath: local2}
	e2 := twoWayEngine(t, d2, "dev", f2)
	e2.reconcileRemoteFolder(f2)
	e2.reconcileRemoteFolder(f2)
	files2, deletes2, _ := d2.snapshot()
	if n := countFiles(t, local2); n != 1 || len(files2) != 2 || len(deletes2) != 0 {
		t.Fatalf("twins: %d here, %d on the device, deletes %v", n, len(files2), deletes2)
	}
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}

	return out
}

// A file that changes while it is sent is a busy file: not sent in
// full again at every pass, but left for a while.
func TestBusyFileWaits(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(nil)
	local := t.TempDir()
	p := filepath.Join(local, "app.log")
	writeFiles(t, local, map[string]string{"app.log": "line 1\n"})
	d.onHas = func() {
		fh, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		_, _ = fh.WriteString("one more line\n")
		_ = fh.Close()
	}
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)

	e.reconcileRemoteFolder(f)
	e.reconcileRemoteFolder(f)
	d.mu.Lock()
	has, begun := 0, len(d.pending)
	for _, ev := range d.events {
		if ev == "has" {
			has++
		}
	}
	d.mu.Unlock()
	if has != 1 || begun != 0 || !e.busyWait(p) {
		t.Fatalf("asked %d times, %d uploads begun, waiting %v", has, begun, e.busyWait(p))
	}
}

// Office's and Explorer's scratch files are never synced, either way.
func TestScratchFilesAreNotSynced(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/folder/THUMBS.DB": []byte("thumbs")})
	local := t.TempDir()
	writeFiles(t, local, map[string]string{"~$report.docx": "owner", "Thumbs.db": "t", "sub/desktop.ini": "ini", "report.docx": "doc"})
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/folder", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	files, deletes, reads := d.snapshot()
	if len(files) != 2 || files["/pc/folder/report.docx"] == nil || reads != 0 || len(deletes) != 0 {
		t.Fatalf("device %q, %d reads, deletes %v", keysOf(files), reads, deletes)
	}
}

func TestSkippedNames(t *testing.T) {
	for rel, want := range map[string]bool{
		"~$report.docx": true, "sub/~$book.xlsx": true, "Thumbs.db": true, "x/DESKTOP.INI": true,
		"ehthumbs.db": true, "report~$.docx": false, "thumbs.db.txt": false, "notes.txt": false,
	} {
		if got := notSynced(rel); got != want {
			t.Errorf("notSynced(%q) = %v, want %v", rel, got, want)
		}
	}
}

// The cache goes by the change time too - an edit that kept the size
// and put the modification time back is read again.
func TestHashCacheNoticesTheChangeTime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no change time on Windows")
	}
	withConfigDir(t)
	e := New(&config.Config{}, "", nil)
	p := filepath.Join(t.TempDir(), "a.txt")
	writeFiles(t, filepath.Dir(p), map[string]string{"a.txt": "aaaa"})
	fi, _ := os.Stat(p)
	if h, err := e.cachedHashInfo("f", p, fi); err != nil || h != sha([]byte("aaaa")) {
		t.Fatal(h, err)
	}
	time.Sleep(20 * time.Millisecond)
	writeFiles(t, filepath.Dir(p), map[string]string{"a.txt": "bbbb"})
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	fi2, _ := os.Stat(p)
	if fi2.Size() != fi.Size() || !fi2.ModTime().Equal(fi.ModTime()) {
		t.Fatal("the test needs the same size and time")
	}
	if h, _ := e.cachedHashInfo("f", p, fi2); h != sha([]byte("bbbb")) {
		t.Fatal("the edit was answered from the cache")
	}
}

// What a pass hashed is written when the engine stops, not only at
// the end of a pass.
func TestHashCacheSavedOnStop(t *testing.T) {
	withConfigDir(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.txt": "a"})
	e := New(&config.Config{RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/x", LocalPath: dir}}}, "", nil)
	if _, err := e.cachedHash("r1", filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	e.Stop()
	p, _ := hashCachePath("r1")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("cache not written at Stop: %v", err)
	}
}

// Folders' passes run a few at a time; one waiting says for which.
func TestPassesWaitForASlot(t *testing.T) {
	withConfigDir(t)
	e := New(&config.Config{}, "", nil)
	yes := func() bool { return true }
	none := func(FolderState) {}
	var releases []func()
	for i, name := range []string{"Alpha", "Beta"} {
		r, ok := e.passSlot(fmt.Sprint(i), "/x/"+name, yes, none)
		if !ok {
			t.Fatal("no slot")
		}
		releases = append(releases, r)
		time.Sleep(5 * time.Millisecond)
	}
	shown := make(chan FolderState, 4)
	got := make(chan bool, 1)
	go func() {
		r, ok := e.passSlot("c", "/x/Gamma", yes, func(s FolderState) { shown <- s })
		if ok {
			r()
		}
		got <- ok
	}()
	select {
	case s := <-shown:
		if s.CurrentFile != "Waiting for Alpha" || s.Msg == nil || s.Msg.Key != "desk.main.waiting_for" {
			t.Fatalf("waiting state %+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no waiting state")
	}
	releases[0]()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("never got a slot")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after a slot was freed")
	}
	releases[1]()
}
