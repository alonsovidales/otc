// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func setOutOfImagesReq(path string, on bool) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 7, Payload: &pb.ReqEnvelope_ReqSetOutOfImages{ReqSetOutOfImages: &pb.SetOutOfImages{Path: path, OutOfImages: on}}}
}

// ownerWithLibrary is a signed-in owner's connection over a mocked
// library database.
func ownerWithLibrary(t *testing.T) (*connHandler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ch := &connHandler{mg: &Manager{filesManager: filesmanager.NewWithDAO(dao.NewWithDB(db))}}
	ch.setSession(newTestAuthenticatedSession(t))
	return ch, mock
}

// Issue #192: SetOutOfImages answers with an Ack; showing a folder inside
// another kept out is refused with out_of_images_by_parent and the
// sentence every client shows; any other failure is a plain error.
// ListOutOfImages lists the flagged folders.
func TestSetOutOfImagesAnswers(t *testing.T) {
	if resp, _ := (&connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}}).processMessage(setOutOfImagesReq("/Photos", true)); resp != nil {
		t.Errorf("before sign-in: %+v", resp)
	}

	ch, mock := ownerWithLibrary(t)
	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Photos/"))

	resp, _ := ch.processMessage(setOutOfImagesReq("/Photos/Trip", false))
	if resp == nil || !resp.Error || resp.ErrorCode != "out_of_images_by_parent" ||
		resp.ErrorMessage != "Trip is inside /Photos, which is kept out of Images - show /Photos in Images to show Trip" {
		t.Errorf("by parent: %+v", resp)
	}

	resp, _ = ch.processMessage(setOutOfImagesReq("/", true))
	if resp == nil || !resp.Error || resp.ErrorCode != "" || !strings.HasPrefix(resp.ErrorMessage, "error updating the folder: ") {
		t.Errorf("the whole library: %+v", resp)
	}

	// A folder with no flag: nothing to do, ok.
	resp, _ = ch.processMessage(setOutOfImagesReq("/Elsewhere", false))
	if ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck); resp.Error || !ok || !ack.RespAck.Ok {
		t.Errorf("showing a folder shown already: %+v", resp)
	}

	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 8, Payload: &pb.ReqEnvelope_ReqListOutOfImages{ReqListOutOfImages: &pb.ListOutOfImages{}}})
	list, ok := resp.GetPayload().(*pb.RespEnvelope_RespOutOfImagesFolders)
	if resp.Error || !ok || strings.Join(list.RespOutOfImagesFolders.Paths, ",") != "/Photos/" {
		t.Errorf("list: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// ListFiles says the device can keep folders out of Images, whether the
// listed folder is, and which entries are; SearchFiles the same, but the
// banner, which belongs to a folder.
func TestListingsCarryOutOfImages(t *testing.T) {
	ch, mock := ownerWithLibrary(t)
	at := time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
	cols := []string{"hash", "mime", "created", "modified", "path", "size"}
	list := func(dir, sub, file string) {
		mock.ExpectQuery("select distinct\\(SUBSTRING_INDEX").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow(sub))
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` >=").
			WillReturnRows(sqlmock.NewRows(cols).AddRow("h1", "image/jpeg", at, at, file, 3))
		mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	}

	list("/Photos/", "/Photos/Private", "/Photos/a.jpg")
	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Photos/Private/"))
	mock.ExpectQuery("select `path`, count\\(\\*\\) from `file_versions`").WillReturnRows(sqlmock.NewRows([]string{"path", "n"}))
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 9, Payload: &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: "/Photos/"}}})
	lof := resp.GetRespListOfFiles()
	if resp.Error || lof == nil || !lof.OutOfImagesSupported || lof.FolderOutOfImages || len(lof.Files) != 2 {
		t.Fatalf("listing /Photos/: %+v", resp)
	}
	for _, f := range lof.Files {
		if f.OutOfImages != (f.Path == "/Photos/Private") {
			t.Errorf("%s: out_of_images %v", f.Path, f.OutOfImages)
		}
	}

	list("/Photos/Private/", "/Photos/Private/Sub", "/Photos/Private/b.jpg")
	mock.ExpectQuery("select `path`, count\\(\\*\\) from `file_versions`").WillReturnRows(sqlmock.NewRows([]string{"path", "n"}))
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 10, Payload: &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: "/Photos/Private/"}}})
	if lof := resp.GetRespListOfFiles(); resp.Error || lof == nil || !lof.FolderOutOfImages || !lof.Files[0].OutOfImages || !lof.Files[1].OutOfImages {
		t.Errorf("listing the folder kept out: %+v", resp)
	}

	mock.ExpectQuery("select `path` from `files` where `path` like \\?").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Photos/Private/b.jpg"))
	mock.ExpectQuery("from `files` where `path` in").WillReturnRows(sqlmock.NewRows(cols).AddRow("h2", "image/jpeg", at, at, "/Photos/Private/b.jpg", 3))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	mock.ExpectQuery("select `path`, count\\(\\*\\) from `file_versions` where `path` in").WillReturnRows(sqlmock.NewRows([]string{"path", "n"}))
	resp, _ = ch.processMessage(searchFilesReq("b.jpg"))
	lof = resp.GetRespListOfFiles()
	if resp.Error || lof == nil || !lof.OutOfImagesSupported || lof.FolderOutOfImages {
		t.Fatalf("search: %+v", resp)
	}
	for _, f := range lof.Files {
		if f.Path == "/Photos/Private/b.jpg" && !f.OutOfImages {
			t.Errorf("a file kept out found by search isn't marked: %v", f)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
