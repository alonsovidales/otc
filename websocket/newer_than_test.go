// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #194: SearchPhotos hands newer_than to the search and says so in
// its answer - oldest_first on every page of it (one going on with its
// token, the last, an empty one) and on no other answer (a plain page, a
// jump's). newer_than wins over before. In a child process, as
// TestSearchPhotosPassesItsThumbnailFlags: it loads a config.
func TestSearchPhotosNewerThanAnswersOldestFirst(t *testing.T) {
	name := "TestSearchPhotosNewerThanAnswersOldestFirst"
	if os.Getenv(cSearchChildEnv) != "1" {
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
	monthEnd := time.Date(2025, 6, 30, 23, 59, 59, 0, time.UTC)
	type photo struct {
		hash, path string
		at         time.Time
	}
	// As the query answers: newest first.
	photos := []photo{
		{fmt.Sprintf("%064x", 0x194c), "/p/c.jpg", monthEnd.AddDate(1, 0, 0)},
		{fmt.Sprintf("%064x", 0x194b), "/p/b.jpg", monthEnd.Add(time.Second)},
		{fmt.Sprintf("%064x", 0x194a), "/p/a.jpg", monthEnd},
	}
	for _, p := range photos {
		path := storage + "/" + p.hash + "_thumbnail"
		if err := blobstore.WriteBytes(path, ses, []byte("thumb")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(path) })
	}
	rows := func(upTo int) *sqlmock.Rows {
		r := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
		for _, p := range photos[3-upTo:] {
			r.AddRow(p.hash, "image/jpeg", p.at, p.at, p.path, 1)
		}
		return r
	}
	copies := "select `hash`, count\\(\\*\\) from `files` where `hash` in \\("
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	// A plain page, then a jump's: its cutoff at the second.
	mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WithoutArgs().WillReturnRows(rows(3))
	mock.ExpectQuery(copies).WillReturnRows(sqlmock.NewRows([]string{"hash", "n"}))
	mock.ExpectQuery("`f`.`created` <= \\?").WithArgs(monthEnd).WillReturnRows(rows(1))
	mock.ExpectQuery(copies).WillReturnRows(sqlmock.NewRows([]string{"hash", "n"}))
	// Up from the jump's instant: the plain query, no date in it; its
	// second page goes on with the token, before or not.
	mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WithoutArgs().WillReturnRows(rows(3))
	mock.ExpectQuery(copies).WillReturnRows(sqlmock.NewRows([]string{"hash", "n"}))
	mock.ExpectQuery(copies).WillReturnRows(sqlmock.NewRows([]string{"hash", "n"}))
	// Up from above everything: an empty page.
	mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WithoutArgs().WillReturnRows(rows(3))

	ch := &connHandler{mg: &Manager{filesManager: filesmanager.NewWithDAO(dao.NewWithDB(db))}}
	ch.setSession(ses)
	jump := timestamppb.New(monthEnd.Add(999 * time.Millisecond)) // the web's end of June
	search := func(what string, req *pb.SearchPhotos, wantPaths string, wantOldestFirst bool) string {
		t.Helper()
		resp, closeConn := ch.processMessage(&pb.ReqEnvelope{Id: 11, Payload: &pb.ReqEnvelope_ReqSearchPhotos{ReqSearchPhotos: req}})
		if closeConn || resp == nil || resp.Error || resp.Id != 11 {
			t.Fatalf("%s: got %+v, close %v", what, resp, closeConn)
		}
		list := resp.GetRespListOfFiles()
		var paths []string
		for _, f := range list.GetFiles() {
			paths = append(paths, f.Path)
			if len(f.Content) == 0 || f.GetCopies() != 1 {
				t.Errorf("%s: %s came without its thumbnail or copies", what, f.Path)
			}
		}
		if got := strings.Join(paths, " "); got != wantPaths || list.GetOldestFirst() != wantOldestFirst {
			t.Fatalf("%s: [%s] oldest_first %v, want [%s] %v", what, got, list.GetOldestFirst(), wantPaths, wantOldestFirst)
		}
		return list.GetToken()
	}

	search("a plain page", &pb.SearchPhotos{}, "/p/c.jpg /p/b.jpg /p/a.jpg", false)
	search("a jump", &pb.SearchPhotos{Before: jump}, "/p/a.jpg", false)
	token := search("up from the jump", &pb.SearchPhotos{NewerThan: jump, Limit: 1}, "/p/b.jpg", true)
	if token == "" {
		t.Fatal("no token after the first page up")
	}
	if last := search("up, its last page", &pb.SearchPhotos{NewerThan: jump, Before: jump, Token: token, Have: 1}, "/p/c.jpg", true); last != "" {
		t.Errorf("token %q after the last page up", last)
	}
	search("up from above everything", &pb.SearchPhotos{NewerThan: timestamppb.New(monthEnd.AddDate(5, 0, 0))}, "", true)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
