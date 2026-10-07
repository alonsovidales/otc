// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func searchFilesReq(query string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 5, Payload: &pb.ReqEnvelope_ReqSearchFiles{ReqSearchFiles: &pb.SearchFiles{Query: query, Limit: 5}}}
}

// Only the owner's session searches the library, as only it lists it:
// nothing answers it before sign-in or for a friend's device, which
// handleConnection turns into not_authenticated.
func TestSearchFilesNeedsTheOwnersSession(t *testing.T) {
	ch := &connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}}
	if resp, _ := ch.processMessage(searchFilesReq("x")); resp != nil {
		t.Errorf("before sign-in: %+v", resp)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `status`, `forget_requested` is not null from `social_friendship`").
		WillReturnRows(sqlmock.NewRows([]string{"status", "leaving"}).AddRow("accepted", false))
	friend := &connHandler{mg: &Manager{dao: dao.NewWithDB(db), filesManager: &filesmanager.Manager{}}}
	friend.setFriendProfile(&profile.Profile{})
	if resp, _ := friend.processMessage(searchFilesReq("x")); resp != nil {
		t.Errorf("a friend: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the friendship was not checked: %v", err)
	}
}

// Signed in, it answers with a ListOfFiles (no token); an empty text with
// no entries, without asking the database.
func TestSearchFilesAnswersAListOfFiles(t *testing.T) {
	ch := &connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}}
	ch.setSession(newTestAuthenticatedSession(t))

	resp, closeConn := ch.processMessage(searchFilesReq("   "))
	if closeConn || resp == nil || resp.Error || resp.Id != 5 {
		t.Fatalf("got %+v, close %v", resp, closeConn)
	}
	list, ok := resp.Payload.(*pb.RespEnvelope_RespListOfFiles)
	if !ok {
		t.Fatalf("expected a RespListOfFiles payload, got %T", resp.Payload)
	}
	if len(list.RespListOfFiles.Files) != 0 || list.RespListOfFiles.Token != "" {
		t.Errorf("expected no entries and no token, got %+v", list.RespListOfFiles)
	}
}
