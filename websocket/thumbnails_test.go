// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// GetThumbnails says where it stopped (ListOfFiles.ask_again_from), so a
// grid asks for those paths again rather than take them for paths without
// a thumbnail: here past the paths a request takes, none of which the
// device has.
func TestGetThumbnailsSaysWhereItStopped(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ch := &connHandler{mg: &Manager{filesManager: filesmanager.NewWithDAO(dao.NewWithDB(db))}}
	ch.setSession(newTestAuthenticatedSession(t))
	paths := make([]string, filesmanager.MaxThumbnailsPerRequest+3)
	for i := range paths {
		paths[i] = fmt.Sprintf("/gone/%d.jpg", i)
	}
	resp, closeConn := ch.processMessage(&pb.ReqEnvelope{Id: 7, Payload: &pb.ReqEnvelope_ReqGetThumbnails{
		ReqGetThumbnails: &pb.GetThumbnails{Paths: paths, SmallThumbnails: true},
	}})
	if closeConn || resp == nil || resp.Error || resp.Id != 7 {
		t.Fatalf("got %+v, close %v", resp, closeConn)
	}
	list := resp.GetRespListOfFiles()
	if list == nil || len(list.Files) != 0 || list.AskAgainFrom != filesmanager.MaxThumbnailsPerRequest {
		t.Errorf("got %+v, want no thumbnails and ask_again_from %d", list, filesmanager.MaxThumbnailsPerRequest)
	}
}

// cSearchChildEnv marks the child process TestSearchPhotosPassesItsThumbnailFlags
// runs in.
const cSearchChildEnv = "OTC_WS_SEARCH_CHILD"

// searchPhotosStorage loads a config holding only what ImageSearch reads -
// [otc] storage-path (a temporary folder) and [tagger] max-images-search -
// since cfg is fatal when nothing is loaded. Only ever in a child process:
// cfg can't be unloaded, and with an [otc] section every later test of
// this binary would behave otherwise (a sign-in starts the thumbnail
// backfill, which finds a storage path to work on).
func searchPhotosStorage(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "otc-wssearch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	storage := filepath.Join(dir, "storage")
	os.MkdirAll(storage, 0o750)
	os.MkdirAll(filepath.Join(dir, "etc"), 0o750)
	ini := "[otc]\nstorage-path=" + storage + "\n[tagger]\nmax-images-search=30\n"
	if err := os.WriteFile(filepath.Join(dir, "etc", "otc_wssearchtest.ini"), []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	os.Chdir(dir)
	err = cfg.Init("otc", "wssearchtest")
	os.Chdir(wd)
	if err != nil {
		t.Fatal(err)
	}
	return storage
}

// SearchPhotos hands small_thumbnails and omit_thumbnails to the search
// each where it belongs (two bools side by side, which nothing else would
// catch swapped): a page omitting thumbnails comes without content, each
// row saying whether a small one is stored; one with small thumbnails
// carries the small one, saying so.
func TestSearchPhotosPassesItsThumbnailFlags(t *testing.T) {
	if os.Getenv(cSearchChildEnv) != "1" {
		// The test itself runs in a child process (searchPhotosStorage).
		name := "TestSearchPhotosPassesItsThumbnailFlags"
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), cSearchChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("in its own process: %v\n%s", err, out)
		}
		return
	}
	storage := searchPhotosStorage(t)
	ses := newTestAuthenticatedSession(t)
	hash := fmt.Sprintf("%064x", 0x5ea4c4)
	big, small := []byte("the big thumbnail"), []byte("the small thumbnail")
	for p, b := range map[string][]byte{"_thumbnail": big, "_thumbnail_small": small} {
		path := storage + "/" + hash + p
		if err := blobstore.WriteBytes(path, ses, b); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(path) })
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(
			sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
				AddRow(hash, "image/jpeg", time.Now(), time.Now(), "/p/a.jpg", 1))
	}
	ch := &connHandler{mg: &Manager{filesManager: filesmanager.NewWithDAO(dao.NewWithDB(db))}}
	ch.setSession(ses)
	search := func(small, omit bool) *pb.File {
		t.Helper()
		resp, closeConn := ch.processMessage(&pb.ReqEnvelope{Id: 9, Payload: &pb.ReqEnvelope_ReqSearchPhotos{
			ReqSearchPhotos: &pb.SearchPhotos{SmallThumbnails: small, OmitThumbnails: omit},
		}})
		if closeConn || resp == nil || resp.Error || resp.Id != 9 {
			t.Fatalf("small %v, omit %v: got %+v, close %v", small, omit, resp, closeConn)
		}
		files := resp.GetRespListOfFiles().GetFiles()
		if len(files) != 1 || files[0].Path != "/p/a.jpg" || files[0].ThumbnailSmall == nil {
			t.Fatalf("small %v, omit %v: got %+v", small, omit, files)
		}
		return files[0]
	}

	if f := search(true, true); f.Content != nil || !f.GetThumbnailSmall() {
		t.Errorf("omitted, small: content %q, small %v; want none, true", f.Content, f.GetThumbnailSmall())
	}
	if f := search(false, true); f.Content != nil || f.GetThumbnailSmall() {
		t.Errorf("omitted, big: content %q, small %v; want none, false", f.Content, f.GetThumbnailSmall())
	}
	if f := search(true, false); !bytes.Equal(f.Content, small) || !f.GetThumbnailSmall() {
		t.Errorf("small: content %q, small %v; want the small one, true", f.Content, f.GetThumbnailSmall())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
