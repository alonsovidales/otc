// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func ptr(b bool) *bool { return &b }

// imagesFixture: an engine over cfg, saved as config.json, linked to a
// fake device - a folder of n files at dir.
func imagesFixture(t *testing.T, n int, cfgFor func(dir string) *config.Config) (*Engine, *fakeDevice, string) {
	t.Helper()
	withConfigDir(t)
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("p%d.jpg", i)), []byte(fmt.Sprint("photo ", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := cfgFor(dir)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	d := &fakeDevice{files: map[string][]byte{}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}}
	conn := connectedEngine(t, d)
	e := New(cfg, "", nil)
	e.ws = conn.ws
	t.Cleanup(func() {
		e.mu.Lock()
		e.stopped = true
		for _, tm := range e.errorRetry {
			tm.Stop()
		}
		for _, tm := range e.remoteRetry {
			tm.Stop()
		}
		for _, w := range e.watchers {
			w.Stop()
		}
		for _, w := range e.remoteWatch {
			w.Stop()
		}
		e.mu.Unlock()
	})
	return e, d, dir
}

func keepOutBackup(dir string) *config.Config {
	return &config.Config{Domain: "dev-a", Folders: []config.Folder{{ID: "b1", Path: dir, OneWay: true, OutOfImages: ptr(true)}}}
}

// sentImages is what the device was asked, as "path=true".
func sentImages(d *fakeDevice) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, r := range d.imagesSet {
		out = append(out, fmt.Sprintf("%s=%v", r.Path, r.OutOfImages))
	}
	return out
}

// onDisk is the folder's request in config.json.
func onDisk(t *testing.T, id string) *bool {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range cfg.Folders {
		if f.ID == id {
			return f.OutOfImages
		}
	}
	for _, f := range cfg.RemoteFolders {
		if f.ID == id {
			return f.OutOfImages
		}
	}
	t.Fatalf("folder %s not in config.json", id)
	return nil
}

func folderImages(e *Engine, id string) config.FolderStatus {
	st := e.Snapshot()
	for _, f := range append(st.Folders, st.RemoteFolders...) {
		if f.ID == id {
			return f
		}
	}
	return config.FolderStatus{}
}

// A backup added to be kept out of Images: the device is asked before
// the first file goes up, once, and never again - not by the next pass,
// nor after a restart (that would undo a change made since from the web).
func TestOutOfImagesSentOnceBeforeTheFirstUpload(t *testing.T) {
	e, d, dir := imagesFixture(t, 3, keepOutBackup)
	f := e.cfg.Folders[0]

	e.setupFolder(f)

	want := []string{e.remotePathFor(dir) + "/=true"}
	if got := sentImages(d); !slices.Equal(got, want) {
		t.Fatalf("asked %v, want %v", got, want)
	}
	d.mu.Lock()
	events := append([]string(nil), d.events...)
	uploaded := len(d.files)
	d.mu.Unlock()
	if first := slices.Index(events, "has"); first < 0 || slices.Index(events, "images") > first || uploaded != 3 {
		t.Fatalf("requests %v, %d files uploaded: the flag must come first", events, uploaded)
	}
	if got := onDisk(t, "b1"); got != nil {
		t.Fatalf("config.json still asks %v once acknowledged", *got)
	}
	if st := folderImages(e, "b1"); st.OutOfImages != string(ImagesKeptOut) {
		t.Fatalf("shown as %q, want kept out (the list read after the ack)", st.OutOfImages)
	}

	e.reconcile(f)
	e.imagesAtConnect()
	// Started again from config.json.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	e2 := New(cfg, "", nil)
	e2.ws = e.ws
	e2.reconcile(cfg.Folders[0])
	e2.imagesAtConnect()
	if got := sentImages(d); len(got) != 1 {
		t.Fatalf("asked again: %v", got)
	}
}

// A device before release 108 answers unknown_payload: the backup goes on,
// the request stays (in config.json too) and is not sent at every pass;
// the folder says the device needs an update. Once it is updated - the
// next connect - the request goes by itself.
func TestOutOfImagesWaitsForADeviceUpdate(t *testing.T) {
	e, d, _ := imagesFixture(t, 2, keepOutBackup)
	f := e.cfg.Folders[0]
	d.imagesAnswer = "unknown_payload"

	e.setupFolder(f)

	d.mu.Lock()
	uploaded := len(d.files)
	d.mu.Unlock()
	if uploaded != 2 {
		t.Fatalf("%d of 2 files backed up: an old device must not stop the backup", uploaded)
	}
	if got := onDisk(t, "b1"); got == nil || !*got {
		t.Fatal("the request was dropped")
	}
	st := e.Snapshot()
	if !st.OutOfImagesUnsupported || st.Folders[0].OutOfImages != string(ImagesUnsupported) {
		t.Fatalf("unsupported %v, folder %q", st.OutOfImagesUnsupported, st.Folders[0].OutOfImages)
	}
	e.reconcile(f)
	if got := sentImages(d); len(got) != 1 {
		t.Fatalf("sent at every pass to a device that can't: %v", got)
	}

	d.mu.Lock()
	d.imagesAnswer = ""
	d.mu.Unlock()
	e.imagesAtConnect()

	if got := sentImages(d); len(got) != 2 {
		t.Fatalf("not sent after the update: %v", got)
	}
	if got := onDisk(t, "b1"); got != nil {
		t.Fatal("still pending once acknowledged")
	}
	if st := e.Snapshot(); st.OutOfImagesUnsupported || st.Folders[0].OutOfImages != string(ImagesKeptOut) {
		t.Fatalf("after the update: unsupported %v, folder %q", st.OutOfImagesUnsupported, st.Folders[0].OutOfImages)
	}
}

// A request to show a two-way folder inside one kept out is refused: it is
// dropped (it can't happen until the folder above is shown), the device's
// message shows on the folder, and it is not sent again.
func TestOutOfImagesRefusedByParentIsDropped(t *testing.T) {
	e, d, _ := imagesFixture(t, 0, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/Photos/Trip", LocalPath: dir, OutOfImages: ptr(false)}}}
	})
	d.imagesAnswer = "out_of_images_by_parent"
	d.keptOut = []string{"/Photos/"}
	f := e.cfg.RemoteFolders[0]

	e.reconcileRemoteFolder(f)

	if got, want := sentImages(d), []string{"/Photos/Trip/=false"}; !slices.Equal(got, want) {
		t.Fatalf("asked %v, want %v", got, want)
	}
	d.mu.Lock()
	events := append([]string(nil), d.events...)
	d.mu.Unlock()
	if slices.Index(events, "images") > slices.Index(events, "list") {
		t.Fatalf("requests %v: the flag goes before the pass lists", events)
	}
	if got := onDisk(t, "r1"); got != nil {
		t.Fatal("the refused request is still pending")
	}
	st := folderImages(e, "r1")
	if st.OutOfImagesNote == "" || st.OutOfImages != string(ImagesKeptOutByParent) || st.OutOfImagesBy != "/Photos" {
		t.Fatalf("folder shows %+v", st)
	}
	e.reconcileRemoteFolder(f)
	if got := sentImages(d); len(got) != 1 {
		t.Fatalf("sent again: %v", got)
	}
}

// Any other failure leaves the request for the next pass, until the
// device takes it.
func TestOutOfImagesRetriedUntilAcknowledged(t *testing.T) {
	e, d, _ := imagesFixture(t, 1, keepOutBackup)
	f := e.cfg.Folders[0]
	d.imagesAnswer = "database is busy"

	e.setupFolder(f)
	if got := onDisk(t, "b1"); got == nil {
		t.Fatal("dropped after a failure")
	}
	if st := folderImages(e, "b1"); st.OutOfImages != string(ImagesKeeping) {
		t.Fatalf("shown as %q while pending", st.OutOfImages)
	}

	d.mu.Lock()
	d.imagesAnswer = ""
	d.mu.Unlock()
	e.reconcile(f)
	e.reconcile(f)

	if got := sentImages(d); len(got) != 2 {
		t.Fatalf("asked %d times, want 2 (the failure, then the one acknowledged)", len(got))
	}
	if got := onDisk(t, "b1"); got != nil {
		t.Fatal("still pending once acknowledged")
	}
}

// Changed from the tray while the request was under way: the change is
// not cleared with the acknowledged one - it goes next.
func TestOutOfImagesCompareAndClear(t *testing.T) {
	e, d, _ := imagesFixture(t, 0, keepOutBackup)
	changed := false
	d.onSetImages = func() {
		if changed {
			return
		}
		changed = true
		// What the tray does: config.json, then the engine's reload.
		cfg, err := config.Load()
		if err != nil {
			t.Error(err)
			return
		}
		cfg.SetOutOfImagesRequest("b1", false)
		if err := cfg.Save(); err != nil {
			t.Error(err)
		}
		e.mu.Lock()
		e.cfg.SetOutOfImagesRequest("b1", false)
		e.mu.Unlock()
	}

	e.applyOutOfImages("b1")

	want := []string{e.remotePathFor(e.cfg.Folders[0].Path) + "/=true", e.remotePathFor(e.cfg.Folders[0].Path) + "/=false"}
	if got := sentImages(d); !slices.Equal(got, want) {
		t.Fatalf("asked %v, want %v", got, want)
	}
	if got := onDisk(t, "b1"); got != nil {
		t.Fatalf("left pending: %v", *got)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.keptOut) != 0 {
		t.Fatalf("the device keeps %v out; the later request was to show it", d.keptOut)
	}
}

// The device acknowledged, but config.json couldn't be cleared (a Windows
// rename-over racing a reader, a read-only disk): nothing sends the
// request again - not the next pass, nor a reload of config.json for an
// unrelated edit, which would undo a change the owner made since from the
// web - and the clear is written once config.json can be. A later request
// with the same value still goes.
func TestOutOfImagesFailedClearIsNotSentAgain(t *testing.T) {
	e, d, _ := imagesFixture(t, 0, keepOutBackup)
	f := e.cfg.Folders[0]
	saveConfig = func(*config.Config) error { return errors.New("access denied") }
	t.Cleanup(func() { saveConfig = (*config.Config).Save })

	e.applyOutOfImages("b1")
	if got := onDisk(t, "b1"); got == nil || !*got {
		t.Fatalf("config.json was written: %v", got)
	}
	if st := folderImages(e, "b1"); st.OutOfImages != string(ImagesKeptOut) {
		t.Fatalf("shown as %q once acknowledged, want kept out", st.OutOfImages)
	}
	e.reconcile(f)
	e.imagesAtConnect()
	if got := sentImages(d); len(got) != 1 {
		t.Fatalf("sent again while config.json still holds it: %v", got)
	}

	// The owner shows the folder from the web; the poll sees it.
	d.mu.Lock()
	d.keptOut = nil
	d.mu.Unlock()
	e.refreshOutOfImages()
	saveConfig = (*config.Config).Save
	// An unrelated edit from the tray: config.json reloaded.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Autostart = ptr(false)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	e.UpdateConfig(cfg, "")
	deadline := time.Now().Add(10 * time.Second)
	for onDisk(t, "b1") != nil {
		if time.Now().After(deadline) {
			t.Fatal("config.json never cleared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.mu.Lock()
	kept := append([]string(nil), d.keptOut...)
	d.mu.Unlock()
	if got := sentImages(d); len(got) != 1 || len(kept) != 0 {
		t.Fatalf("the reload sent it again: asked %v, the device keeps %v out", got, kept)
	}
	if st := folderImages(e, "b1"); st.OutOfImages != string(ImagesShown) {
		t.Fatalf("shown as %q, want shown (the owner's change)", st.OutOfImages)
	}

	// Kept out again on purpose, from the tray: that goes.
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetOutOfImagesRequest("b1", true)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	e.UpdateConfig(cfg, "")
	for len(sentImages(d)) != 2 || onDisk(t, "b1") != nil {
		if time.Now().After(deadline) {
			t.Fatalf("the new request: asked %v, pending %v", sentImages(d), onDisk(t, "b1"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The device's refusal to show a folder inside one kept out is about that
// folder above: once the owner shows it (from the web), the refusal goes
// from the folder's line with the next poll.
func TestOutOfImagesRefusalGoesWithTheFolderAbove(t *testing.T) {
	e, d, _ := imagesFixture(t, 0, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/Photos/Trip", LocalPath: dir, OutOfImages: ptr(false)}}}
	})
	d.imagesAnswer = "out_of_images_by_parent"
	d.keptOut = []string{"/Photos/"}
	e.reconcileRemoteFolder(e.cfg.RemoteFolders[0])
	if st := folderImages(e, "r1"); st.OutOfImagesNote == "" {
		t.Fatalf("no refusal shown: %+v", st)
	}

	d.mu.Lock()
	d.keptOut = nil
	d.imagesAnswer = ""
	d.mu.Unlock()
	e.refreshOutOfImages()

	st := folderImages(e, "r1")
	if st.OutOfImages != string(ImagesShown) || st.OutOfImagesNote != "" {
		t.Fatalf("after the folder above was shown: %q, note %q", st.OutOfImages, st.OutOfImagesNote)
	}
	// Kept out again later: the old refusal doesn't come back with it.
	d.mu.Lock()
	d.keptOut = []string{"/Photos/"}
	d.mu.Unlock()
	e.refreshOutOfImages()
	if st := folderImages(e, "r1"); st.OutOfImages != string(ImagesKeptOutByParent) || st.OutOfImagesNote != "" {
		t.Fatalf("kept out by /Photos again: %q, note %q", st.OutOfImages, st.OutOfImagesNote)
	}
}

// Changed from the tray while a request was under way, and that request
// failed: the change goes at once, not at the folder's next pass - the
// tray's own call found the folder busy and only waited.
func TestOutOfImagesChangeDuringAFailedRequestGoesNow(t *testing.T) {
	e, d, _ := imagesFixture(t, 0, keepOutBackup)
	d.imagesAnswer = "database is busy"
	waited := make(chan struct{})
	first := true
	d.onSetImages = func() { // d.mu is held
		if !first {
			d.imagesAnswer = "" // only the first one fails
			return
		}
		first = false
		cfg, err := config.Load()
		if err != nil {
			t.Error(err)
			return
		}
		cfg.SetOutOfImagesRequest("b1", false)
		if err := cfg.Save(); err != nil {
			t.Error(err)
		}
		e.mu.Lock()
		e.cfg.SetOutOfImagesRequest("b1", false)
		e.mu.Unlock()
		// What UpdateConfig starts for the change.
		go func() {
			e.applyPendingOutOfImages("b1")
			close(waited)
		}()
	}
	e.applyOutOfImages("b1")
	<-waited

	path := e.remotePathFor(e.cfg.Folders[0].Path) + "/"
	if got, want := sentImages(d), []string{path + "=true", path + "=false"}; !slices.Equal(got, want) {
		t.Fatalf("asked %v, want %v", got, want)
	}
	if got := onDisk(t, "b1"); got != nil {
		t.Fatalf("left pending: %v", *got)
	}
}

// A request made from the tray or the command line for a folder already
// synced goes as config.json brings it, not at the next pass.
func TestOutOfImagesFromTheMenuGoesAtOnce(t *testing.T) {
	e, d, _ := imagesFixture(t, 1, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", Folders: []config.Folder{{ID: "b1", Path: dir, OneWay: true}}}
	})
	e.setupFolder(e.cfg.Folders[0])
	if got := sentImages(d); len(got) != 0 {
		t.Fatalf("asked %v with nothing requested", got)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetOutOfImagesRequest("b1", true)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	e.UpdateConfig(cfg, "")

	deadline := time.Now().Add(10 * time.Second)
	for len(sentImages(d)) == 0 || onDisk(t, "b1") != nil {
		if time.Now().After(deadline) {
			t.Fatalf("not sent: %v, pending %v", sentImages(d), onDisk(t, "b1"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := sentImages(d); len(got) != 1 {
		t.Fatalf("sent %d times", len(got))
	}
}

// Pointed at another device while the request was under way: the old
// device's answer doesn't clear it - the new one gets it too.
func TestOutOfImagesAnswerOfAnotherDeviceIgnored(t *testing.T) {
	e, d, _ := imagesFixture(t, 0, keepOutBackup)
	d.onSetImages = func() {
		e.mu.Lock()
		e.cfg.Domain = "dev-b"
		e.mu.Unlock()
	}

	e.applyOutOfImages("b1")

	if got := onDisk(t, "b1"); got == nil {
		t.Fatal("cleared by another device's answer")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.Folders[0].OutOfImages == nil {
		t.Fatal("cleared in memory by another device's answer")
	}
}

// A two-way folder added from this computer arrives as a Folder and is
// moved over: its request goes with it.
func TestMigrateFoldersCarriesOutOfImages(t *testing.T) {
	withConfigDir(t)
	cfg := &config.Config{Folders: []config.Folder{{ID: "f1", Path: "/home/ana/Scans", OutOfImages: ptr(true)}}}
	New(cfg, "", nil)
	if len(cfg.RemoteFolders) != 1 || cfg.RemoteFolders[0].OutOfImages == nil || !*cfg.RemoteFolders[0].OutOfImages {
		t.Fatalf("migrated to %+v", cfg.RemoteFolders)
	}
	saved, err := config.Load()
	if err != nil || len(saved.RemoteFolders) != 1 || saved.RemoteFolders[0].OutOfImages == nil {
		t.Fatalf("config.json: %+v %v", saved, err)
	}
}

func TestOutOfImagesState(t *testing.T) {
	yes, no := ptr(true), ptr(false)
	folders := []string{"/mac/Studio/Users/me/Scans/", "/Photos/"}
	cases := []struct {
		name      string
		path      string
		folders   []string
		pending   *bool
		supported *bool
		want      ImagesState
		by        string
	}{
		{"not listed yet", "/Photos/", nil, nil, nil, ImagesUnknown, ""},
		{"own flag", "/Photos/", folders, nil, yes, ImagesKeptOut, ""},
		{"supported not asked yet", "/Photos/", folders, nil, nil, ImagesKeptOut, ""},
		{"by the folder above", "/Photos/Trip/", folders, nil, yes, ImagesKeptOutByParent, "/Photos"},
		{"own flag inside a flagged one", "/Photos/Trip/", append(folders, "/Photos/Trip/"), nil, yes, ImagesKeptOutByParent, "/Photos"},
		{"nearest above", "/a/b/c/", []string{"/a/", "/a/b/"}, nil, yes, ImagesKeptOutByParent, "/a/b"},
		{"a sibling's prefix is not a parent", "/Photos2/", folders, nil, yes, ImagesShown, ""},
		{"exact bytes", "/photos/", folders, nil, yes, ImagesShown, ""},
		{"shown", "/Documents/", folders, nil, yes, ImagesShown, ""},
		{"none kept out", "/Documents/", []string{}, nil, yes, ImagesShown, ""},
		{"keeping", "/Documents/", folders, yes, yes, ImagesKeeping, ""},
		{"showing", "/Photos/", folders, no, yes, ImagesShowing, ""},
		{"pending offline", "/Documents/", nil, yes, nil, ImagesKeeping, ""},
		{"pending on an old device", "/Documents/", nil, yes, no, ImagesUnsupported, ""},
		{"old device, nothing asked", "/Documents/", nil, nil, no, ImagesUnknown, ""},
	}
	for _, c := range cases {
		got, by := OutOfImagesState(c.path, c.folders, c.pending, c.supported)
		if got != c.want || by != c.by {
			t.Errorf("%s: got %q %q, want %q %q", c.name, got, by, c.want, c.by)
		}
	}
	if !ImagesKeeping.Kept() || !ImagesKeptOutByParent.Kept() || ImagesShowing.Kept() || ImagesUnsupported.Kept() {
		t.Error("Kept")
	}
}

// fakeDeviceURL serves d at a ws:// address, for an engine that connects
// by itself (connectedEngine's server, without its engine).
func fakeDeviceURL(t *testing.T, d *fakeDevice) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			req := &pb.ReqEnvelope{}
			if proto.Unmarshal(data, req) != nil {
				return
			}
			b, _ := proto.Marshal(d.handle(req, pubDER))
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

// The real connect: what the device keeps out is asked, a request still
// pending (added while offline) goes - before the folder's first upload -
// and the menu shows the folder kept out.
func TestOutOfImagesAtConnect(t *testing.T) {
	withConfigDir(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.jpg"), []byte("photo"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &fakeDevice{files: map[string][]byte{}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}}
	cfg := &config.Config{Domain: fakeDeviceURL(t, d), ClientID: "test", Folders: []config.Folder{{ID: "b1", Path: dir, OneWay: true, OutOfImages: ptr(true)}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	e := New(cfg, "secret", nil)
	t.Cleanup(e.Stop)
	e.applySettings()

	deadline := time.Now().Add(15 * time.Second)
	for {
		d.mu.Lock()
		uploaded := len(d.files)
		d.mu.Unlock()
		st := folderImages(e, "b1")
		if uploaded == 1 && st.OutOfImages == string(ImagesKeptOut) && onDisk(t, "b1") == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d files up, folder %q, pending %v, asked %v", uploaded, st.OutOfImages, onDisk(t, "b1"), sentImages(d))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := sentImages(d); len(got) != 1 {
		t.Fatalf("asked %v", got)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if slices.Index(d.events, "images") > slices.Index(d.events, "has") {
		t.Fatalf("requests %v: the flag must come before the first upload", d.events)
	}
}
