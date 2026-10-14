// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func folderUsageReq(path string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 11, Payload: &pb.ReqEnvelope_ReqGetFolderUsage{ReqGetFolderUsage: &pb.GetFolderUsage{Path: path}}}
}

// Issue #195: a signed-in session gets the folder's FolderUsage - its path
// with the trailing slash, the entries largest first and the totals.
func TestGetFolderUsageAnswers(t *testing.T) {
	ch, mock := ownerWithLibrary(t)
	mock.ExpectQuery("select substring_index\\(`path`, '/', \\?\\).* from `files` where .* union all select .* from `file_versions` where").
		WithArgs(3, 3, "/Photos/", "/Photos/", "/Photos/%", 3, 3, "/Photos/", "/Photos/", "/Photos/%").
		WillReturnRows(sqlmock.NewRows([]string{"child", "dir", "size", "version"}).
			AddRow("/Photos/a.jpg", 0, int64(10), 0).
			AddRow("/Photos/2024", 1, int64(300), 0).
			AddRow("/Photos/2024", 1, int64(200), 0).
			AddRow("/Photos/2024", 1, int64(7), 1))

	resp, closeConn := ch.processMessage(folderUsageReq("/Photos"))
	if closeConn || resp == nil || resp.Error || resp.Id != 11 {
		t.Fatalf("got %+v, close %v", resp, closeConn)
	}
	usage := resp.GetRespFolderUsage()
	if usage == nil {
		t.Fatalf("expected a FolderUsage, got %T", resp.Payload)
	}
	if usage.Path != "/Photos/" || usage.FilesSize != 510 || usage.FileCount != 3 || usage.VersionsSize != 7 || len(usage.Entries) != 2 {
		t.Fatalf("got %+v", usage)
	}
	if e := usage.Entries[0]; e.Path != "/Photos/2024" || !e.IsDir || e.FilesSize != 500 || e.FileCount != 2 || e.VersionsSize != 7 {
		t.Errorf("first entry: %+v", e)
	}
	if e := usage.Entries[1]; e.Path != "/Photos/a.jpg" || e.IsDir || e.FilesSize != 10 || e.FileCount != 1 || e.VersionsSize != 0 {
		t.Errorf("second entry: %+v", e)
	}

	// A database error is a plain error reply, with no code.
	mock.ExpectQuery("from `files` where").WillReturnError(errors.New("connection lost"))
	resp, closeConn = ch.processMessage(folderUsageReq("/Photos/"))
	if closeConn || resp == nil || !resp.Error || resp.ErrorCode != "" || resp.ErrorMessage != "connection lost" || resp.Payload != nil {
		t.Errorf("a failure: %+v, close %v", resp, closeConn)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Only a signed-in session sizes folders, as only it lists them: nothing
// answers before sign-in or for a friend's device (handleConnection turns
// that into not_authenticated), and the library isn't read.
func TestGetFolderUsageNeedsASession(t *testing.T) {
	ch := &connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}}
	if resp, _ := ch.processMessage(folderUsageReq("/")); resp != nil {
		t.Errorf("before sign-in: %+v", resp)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `status`, `forget_requested` is not null from `social_friendship`").
		WillReturnRows(sqlmock.NewRows([]string{"status", "leaving"}).AddRow("accepted", false))
	d := dao.NewWithDB(db)
	friend := &connHandler{mg: &Manager{dao: d, filesManager: filesmanager.NewWithDAO(d)}}
	friend.setFriendProfile(&profile.Profile{})
	if resp, _ := friend.processMessage(folderUsageReq("/")); resp != nil {
		t.Errorf("a friend: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the friendship was not checked, or the library was read: %v", err)
	}
}
