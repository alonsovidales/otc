// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/modelfetch"
	"github.com/alonsovidales/otc/modelserver"
	"github.com/alonsovidales/otc/session"
)

// stubSource is a smartModelSource the tests drive.
type stubSource struct {
	mu    sync.Mutex
	st    smartModelStatus
	err   error
	wants []bool
	model modelserver.Embedder
}

func (s *stubSource) want(on bool) (smartModelStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wants = append(s.wants, on)
	return s.st, s.err
}

func (s *stubSource) status() smartModelStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

func (s *stubSource) embedder() modelserver.Embedder { return s.model }

func (s *stubSource) set(st smartModelStatus) {
	s.mu.Lock()
	s.st = st
	s.mu.Unlock()
}

func (s *stubSource) asked() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprint(s.wants)
}

// recordBackfills replaces smartBackfill: the sessions it was started with.
func recordBackfills(t *testing.T) func() []*session.Session {
	t.Helper()
	var mu sync.Mutex
	var got []*session.Session
	prev := smartBackfill
	smartBackfill = func(_ *Manager, ses *session.Session) {
		mu.Lock()
		got = append(got, ses)
		mu.Unlock()
	}
	t.Cleanup(func() { smartBackfill = prev })
	return func() []*session.Session {
		mu.Lock()
		defer mu.Unlock()
		return append([]*session.Session(nil), got...)
	}
}

func smartMock(t *testing.T, on bool) (*Manager, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mock.ExpectQuery("select `smart_search_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"on"}).AddRow(on))
	return &Manager{dao: dao.NewWithDB(db)}, mock
}

// Turned on without the model: stored on and answered at once, the model
// asked for; Settings says how far its download is and why the last try
// failed, a text search that it is downloading; once it is there the photos
// are described with the session that turned it on.
func TestSmartSearchOnDownloadsTheModelFirst(t *testing.T) {
	backfills := recordBackfills(t)
	ses := faceTestSession(t)
	mg, mock := smartMock(t, false)
	src := &stubSource{st: smartModelStatus{downloading: true, percent: 37, problem: &modelfetch.Problem{Kind: modelfetch.Unreachable}}}
	mg.smartSrc = src
	mock.ExpectExec("update `settings` set `smart_search_enabled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.SetSmartSearchEnabled(ses, true); err != nil || !mg.SmartSearchEnabled() {
		t.Fatalf("on: %v", err)
	}
	if src.asked() != "[true]" {
		t.Errorf("asked %s", src.asked())
	}
	if len(backfills()) != 0 {
		t.Error("described without the model")
	}
	m := mg.SmartSearchModel("en")
	if m.Ready || !m.Downloading || m.Percent != 37 || m.Problem != i18n.DevFilesSmartSearchModelUnreachable().Render("en") {
		t.Errorf("Settings: %+v", m)
	}
	if es := mg.SmartSearchModel("es"); es.Problem == m.Problem || es.Problem == "" {
		t.Errorf("the problem in Spanish: %q", es.Problem)
	}
	_, _, err := mg.ImageSearchText("snow", nil, ses, "", nil, "", false, nil, "", nil, nil, 0, 0, false, false)
	if !errors.Is(err, ErrSmartSearchDownloading) || SmartSearchDownloadPercent(err) != 37 {
		t.Errorf("a text search while downloading: %v", err)
	}
	// There now: the model is this instance's, and the photos described.
	src.model = &fakeEmbedder{text: []float32{1, 0, 0}}
	src.set(smartModelStatus{ready: true})
	mg.adoptSmartModel()
	if mg.smartEmbedder() == nil || !mg.SmartSearchAvailable() {
		t.Fatal("the model wasn't taken")
	}
	if got := backfills(); len(got) != 1 || got[0] != ses {
		t.Errorf("backfills %v", got)
	}
	if mg.smart.wait.pending.Load() {
		t.Error("left for a sign-in too")
	}
	if m := mg.SmartSearchModel("en"); !m.Ready || m.Downloading || m.Problem != "" {
		t.Errorf("Settings once there: %+v", m)
	}
	// Taken once.
	mg.adoptSmartModel()
	if len(backfills()) != 1 {
		t.Error("described twice")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Left on with the model on its way (a restart, the update after a failed
// download): the primary asks for it at start, and the next sign-in
// describes the photos once it is there. A session from before a password
// change isn't used.
func TestSmartSearchModelArrivesWithNobodySignedIn(t *testing.T) {
	backfills := recordBackfills(t)
	mg, _ := smartMock(t, true)
	src := &stubSource{st: smartModelStatus{downloading: true}}
	mg.smartSrc = src
	mg.resumeSmartModel()
	if src.asked() != "[true]" {
		t.Fatalf("asked %s at start", src.asked())
	}
	ses := faceTestSession(t)
	mg.SmartSearchSignedIn(ses) // before the model: nothing to do
	src.model = &fakeEmbedder{}
	src.set(smartModelStatus{ready: true})
	mg.adoptSmartModel()
	if len(backfills()) != 0 || !mg.smart.wait.pending.Load() {
		t.Fatalf("with nobody's session: %v %v", backfills(), mg.smart.wait.pending.Load())
	}
	mg.SmartSearchSignedIn(ses)
	mg.SmartSearchSignedIn(ses)
	if got := backfills(); len(got) != 1 || got[0] != ses {
		t.Errorf("at the sign-in: %v", got)
	}

	backfills = recordBackfills(t)
	mg, mock := smartMock(t, false)
	src = &stubSource{st: smartModelStatus{downloading: true}}
	mg.smartSrc = src
	mock.ExpectExec("update `settings` set `smart_search_enabled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.SetSmartSearchEnabled(ses, true); err != nil {
		t.Fatal(err)
	}
	session.PasswordChanged()
	src.model = &fakeEmbedder{}
	src.set(smartModelStatus{ready: true})
	mg.adoptSmartModel()
	if len(backfills()) != 0 {
		t.Error("described with a session from before the password change")
	}
	later := faceTestSession(t)
	mg.SmartSearchSignedIn(later)
	if got := backfills(); len(got) != 1 || got[0] != later {
		t.Errorf("at the next sign-in: %v", got)
	}
}

// Off while it downloads: the descriptions go as always, the instance no
// longer wants the model, and the session held for it is forgotten.
func TestSmartSearchOffWhileDownloading(t *testing.T) {
	backfills := recordBackfills(t)
	ses := faceTestSession(t)
	mg, mock := smartMock(t, false)
	src := &stubSource{st: smartModelStatus{downloading: true}}
	mg.smartSrc = src
	mock.ExpectExec("update `settings` set `smart_search_enabled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectExec("update `settings` set `smart_search_enabled` = 0").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `photo_embeddings`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	if err := mg.SetSmartSearchEnabled(ses, true); err != nil {
		t.Fatal(err)
	}
	if err := mg.SetSmartSearchEnabled(ses, false); err != nil || mg.SmartSearchEnabled() {
		t.Fatalf("off: %v", err)
	}
	if src.asked() != "[true false]" {
		t.Errorf("asked %s", src.asked())
	}
	src.model = &fakeEmbedder{}
	src.set(smartModelStatus{ready: true})
	mg.adoptSmartModel()
	mg.SmartSearchSignedIn(ses)
	if len(backfills()) != 0 || mg.smart.wait.pending.Load() {
		t.Error("described after off")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A device that can't get the model refuses to turn it on, and stores
// nothing; one whose primary can't be asked says why.
func TestSmartSearchWithoutAWayToTheModel(t *testing.T) {
	mg, mock := smartMock(t, false)
	mg.smartSrc = &stubSource{st: smartModelStatus{unavailable: true}}
	if err := mg.SetSmartSearchEnabled(nil, true); !errors.Is(err, ErrSmartSearchUnavailable) {
		t.Errorf("unavailable: %v", err)
	}
	broken := errors.New("the shared models aren't reachable")
	mg.smartSrc = &stubSource{err: broken}
	if err := mg.SetSmartSearchEnabled(nil, true); !errors.Is(err, broken) {
		t.Errorf("unreachable primary: %v", err)
	}
	if mg.SmartSearchEnabled() {
		t.Error("stored on")
	}
	if m := (&Manager{}).SmartSearchModel("en"); m.Ready || m.Downloading {
		t.Errorf("no source: %+v", m)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// tinyModel replaces smartModelFiles with small files served by a test
// server: their contents by asset, and the server's address. block, when
// set, holds that asset's answer until released.
type tinyModel struct {
	content map[string][]byte
	url     string
	mu      sync.Mutex
	asked   []string
	block   string
	release chan struct{}
}

func newTinyModel(t *testing.T) *tinyModel {
	t.Helper()
	tm := &tinyModel{content: map[string][]byte{}, release: make(chan struct{})}
	prev := smartModelFiles
	var files []modelfetch.File
	for i, f := range prev {
		b := []byte(strings.Repeat(f.Name, 100+i*500))
		sum := sha256.Sum256(b)
		files = append(files, modelfetch.File{Name: f.Name, Asset: f.Asset, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])})
		tm.content[f.Asset] = b
	}
	smartModelFiles = files
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asset := strings.TrimPrefix(r.URL.Path, "/models/")
		tm.mu.Lock()
		tm.asked = append(tm.asked, asset)
		block := tm.block
		tm.mu.Unlock()
		b, ok := tm.content[asset]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if asset == block {
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			w.Write(b[:10])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-tm.release:
			}
			w.Write(b[10:])
			return
		}
		w.Write(b)
	}))
	tm.url = srv.URL + "/models/"
	t.Cleanup(func() {
		smartModelFiles = prev
		srv.Close()
	})
	return tm
}

func (tm *tinyModel) wasAsked(asset string) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, a := range tm.asked {
		if a == asset {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The primary downloads the model while any instance wants it - its own
// switch or a user's instance - stops once none does (keeping the whole
// files, not the partial one), and takes it once it is there.
func TestThePrimaryDownloadsWhileAnyInstanceWantsIt(t *testing.T) {
	tm := newTinyModel(t)
	tm.block = "siglip2-base-text.onnx.data"
	dir := filepath.Join(t.TempDir(), "siglip2-base")
	mg := &Manager{}
	local := newLocalModel(smartModelConf{dir: dir, url: tm.url}, false, nil, mg.adoptSmartModel)
	mg.smartLocal, mg.smartSrc = local, local
	if st := local.status(); st.ready || st.downloading || st.unavailable {
		t.Fatalf("before anyone asked: %+v", st)
	}
	if w := mg.smartModelWire(); !w.CanFetch || w.Downloading {
		t.Errorf("info before anyone asked: %+v", w)
	}
	if st, _ := local.want(true); !st.downloading {
		t.Fatalf("on: %+v", st)
	}
	waitFor(t, "the text tower's weights", func() bool { return tm.wasAsked(tm.block) })
	if w := mg.serveSmartModel("4b0e", true); !w.CanFetch || !w.Downloading || w.Percent <= 0 || w.Percent >= 100 {
		t.Errorf("a user's ask: %+v", w)
	}
	// The primary's own switch off: the user still wants it.
	if st, _ := local.want(false); !st.downloading {
		t.Errorf("off on the primary, on for a user: %+v", st)
	}
	// Nobody now: stopped, the part removed, the whole files kept.
	if w := mg.serveSmartModel("4b0e", false); w.Downloading {
		t.Errorf("off everywhere: %+v", w)
	}
	if _, err := os.Stat(filepath.Join(dir, ".text.onnx.data.part")); err == nil {
		t.Error("the partial file was kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "image.onnx.data")); err != nil {
		t.Errorf("a whole file was removed: %v", err)
	}
	if mg.smartEmbedder() != nil {
		t.Fatal("a model without its files")
	}
	// On again, and let through: the model is the primary's.
	close(tm.release)
	mg.serveSmartModel("4b0e", true)
	waitFor(t, "the model", func() bool { return mg.smartEmbedder() != nil })
	if !local.ready() || local.paths.text != filepath.Join(dir, "text.onnx") {
		t.Errorf("ready %v, text tower at %s", local.ready(), local.paths.text)
	}
	if w := mg.serveSmartModel("4b0e", true); !w.CanFetch || w.Downloading {
		t.Errorf("once there: %+v", w)
	}
}

// A user's instance that stops asking no longer counts after cSmartLease.
func TestAUsersAskRunsOut(t *testing.T) {
	m := &localModel{leases: map[string]time.Time{}}
	now := time.Unix(1e9, 0)
	m.now = func() time.Time { return now }
	m.wantFor("4b0e", true)
	if !m.wanted() {
		t.Fatal("not wanted after an ask")
	}
	now = now.Add(cSmartLease - time.Second)
	if !m.wanted() {
		t.Error("forgotten too soon")
	}
	now = now.Add(2 * time.Second)
	if m.wanted() || len(m.leases) != 0 {
		t.Error("still wanted after the lease ran out")
	}
	m.wantFor("", true)
	now = now.Add(24 * time.Hour)
	if !m.wanted() {
		t.Error("the primary's own switch ran out")
	}
}

// [embedder]'s files are used where they are - each tower with its .data
// beside it - and only what isn't there is downloaded; without anywhere to
// download it the model is unavailable.
func TestTheConfiguredFilesAreUsedWhereTheyAre(t *testing.T) {
	usr := t.TempDir()
	put := func(name string) string {
		p := filepath.Join(usr, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	image := put("image.onnx")
	put("image.onnx.data")
	text := put("text.onnx") // without its .data: downloaded
	tok := put("tokenizer.model")
	dir := filepath.Join(t.TempDir(), "dl")
	c := smartModelConf{configured: embedderFiles{image: image, text: text, tokenizer: tok}, dir: dir, url: "https://example.invalid/"}
	m := newLocalModel(c, false, nil, nil)
	if m.paths.image != image || m.paths.tokenizer != tok || m.paths.text != filepath.Join(dir, "text.onnx") {
		t.Errorf("paths %+v", m.paths)
	}
	if m.ready() || m.fetch == nil {
		t.Fatal("ready without the text tower's weights")
	}
	put("text.onnx.data")
	m = newLocalModel(c, false, nil, nil)
	if !m.ready() || m.fetch != nil || m.paths.text != text {
		t.Errorf("every file there: ready %v, paths %+v", m.ready(), m.paths)
	}
	c = smartModelConf{}
	m = newLocalModel(c, false, nil, nil)
	if st := m.status(); !st.unavailable {
		t.Errorf("nowhere to download: %+v", st)
	}
	if st := m.wantFor("", true); !st.unavailable {
		t.Errorf("asked, nowhere to download: %+v", st)
	}
}

// A user's instance asks the primary over the model socket, follows its
// download in Settings and takes the primary's model once it is there; a
// primary that can't download it (or one older than that) has it or not.
func TestAUsersInstanceFollowsThePrimarysDownload(t *testing.T) {
	backfills := recordBackfills(t)
	sockDir, err := os.MkdirTemp("/tmp", "otcsm") // short: Unix socket paths are length-limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	var mu sync.Mutex
	var model modelserver.Embedder
	var asked []string
	state := modelserver.EmbedderState{CanFetch: true, Downloading: true, Percent: 12, Problem: modelfetch.NoSpace, Need: 300 << 20, Left: 20 << 20}
	path := filepath.Join(sockDir, "m.sock")
	go modelserver.ServeModels(path, modelserver.Models{
		Tagger: func() modelserver.Tagger { return nil },
		Embedder: func() modelserver.Embedder {
			mu.Lock()
			defer mu.Unlock()
			return model
		},
		EmbedderFetch: func(who string, want bool) modelserver.EmbedderState {
			mu.Lock()
			defer mu.Unlock()
			asked = append(asked, fmt.Sprint(who, " ", want))
			return state
		},
		EmbedderState: func() modelserver.EmbedderState {
			mu.Lock()
			defer mu.Unlock()
			return state
		},
	}, 1)
	waitFor(t, "the socket", func() bool { _, err := os.Stat(path); return err == nil })
	client := &modelserver.Client{Path: path}

	ses := faceTestSession(t)
	mg, mock := smartMock(t, false)
	mg.smartSrc = &remoteModel{client: client, who: "4b0e"}
	mock.ExpectExec("update `settings` set `smart_search_enabled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.SetSmartSearchEnabled(ses, true); err != nil {
		t.Fatalf("on: %v", err)
	}
	m := mg.SmartSearchModel("en")
	if !m.Downloading || m.Percent != 12 || m.Problem != i18n.DevFilesSmartSearchModelNoSpace(300<<20, 20<<20).Render("en") {
		t.Errorf("Settings while the primary downloads: %+v", m)
	}
	mu.Lock()
	model = &fakeEmbedder{text: []float32{1, 0, 0}}
	state = modelserver.EmbedderState{CanFetch: true}
	mu.Unlock()
	if m := mg.SmartSearchModel("en"); !m.Ready {
		t.Errorf("Settings once the primary has it: %+v", m)
	}
	if mg.smartEmbedder() != client {
		t.Error("the primary's model wasn't taken")
	}
	if got := backfills(); len(got) != 1 || got[0] != ses {
		t.Errorf("backfills %v", got)
	}
	mu.Lock()
	if len(asked) == 0 || asked[0] != "4b0e true" {
		t.Errorf("the primary was asked %v", asked)
	}
	mu.Unlock()

	// A primary that can't download it: has it, or it is unavailable.
	path2 := filepath.Join(sockDir, "n.sock")
	var has modelserver.Embedder
	var hasMu sync.Mutex
	go modelserver.ServeModels(path2, modelserver.Models{Tagger: func() modelserver.Tagger { return nil }, Embedder: func() modelserver.Embedder {
		hasMu.Lock()
		defer hasMu.Unlock()
		return has
	}}, 1)
	waitFor(t, "the second socket", func() bool { _, err := os.Stat(path2); return err == nil })
	r := &remoteModel{client: &modelserver.Client{Path: path2}, who: "4b0e"}
	if st, err := r.want(true); err != nil || !st.unavailable {
		t.Errorf("a primary without the model or its download: %+v %v", st, err)
	}
	hasMu.Lock()
	has = &fakeEmbedder{}
	hasMu.Unlock()
	if st, err := r.want(true); err != nil || !st.ready {
		t.Errorf("a primary with the model and no download: %+v %v", st, err)
	}
}
