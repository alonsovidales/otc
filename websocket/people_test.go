// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func removeFromPersonReq(person string, hashes []string, to, lang string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 9, Lang: lang, Payload: &pb.ReqEnvelope_ReqRemoveFromPerson{ReqRemoveFromPerson: &pb.RemoveFromPerson{
		PersonId: person, Hashes: hashes, ToPersonId: to,
	}}}
}

// "Not this person" / "Someone else…" answer with the generic Ack; what
// can't be right is refused before the database is asked, in the
// request's language; a person picked who is gone meanwhile is said so.
func TestRemoveFromPersonAnswers(t *testing.T) {
	if resp, _ := (&connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}}).processMessage(removeFromPersonReq("p1", []string{"A"}, "", "")); resp != nil {
		t.Errorf("before sign-in: %+v", resp)
	}

	ch, mock := ownerWithLibrary(t)
	tooMany := make([]string, cMaxRemoveFromPerson+1)
	for _, c := range []struct {
		name string
		req  *pb.ReqEnvelope
		want i18n.Msg
	}{
		{"no person", removeFromPersonReq("", []string{"A"}, "", ""), i18n.DevFilesPersonMissing()},
		{"no photos", removeFromPersonReq("p1", nil, "p2", ""), i18n.DevFilesPersonNoPhotos()},
		{"too many photos", removeFromPersonReq("p1", tooMany, "", "fr"), i18n.DevFilesPersonTooMany(cMaxRemoveFromPerson)},
		{"to themselves", removeFromPersonReq("p1", []string{"A"}, "p1", "es"), i18n.DevFilesPersonSame()},
	} {
		lang := c.req.Lang
		resp, _ := ch.processMessage(c.req)
		if resp == nil || !resp.Error || resp.ErrorMessage != c.want.Render(lang) || resp.Payload != nil {
			t.Errorf("%s: %+v, want %q", c.name, resp, c.want.Render(lang))
		}
	}
	if got := i18n.DevFilesPersonSame().Render("es"); got == i18n.DevFilesPersonSame().Render("") {
		t.Errorf("the refusal isn't in Spanish: %q", got)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs("gone").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectRollback()
	resp, _ := ch.processMessage(removeFromPersonReq("p1", []string{"A"}, "gone", ""))
	if resp == nil || !resp.Error || resp.ErrorMessage != "that person is no longer on this device" {
		t.Errorf("to a person who is gone: %+v", resp)
	}

	// Photos without a face of the person, nor the person added to them
	// by hand: nothing to do, ok.
	mock.ExpectBegin()
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\? and `hash` in").WithArgs("p1", "A").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec("delete from `people_by_hand` where `person_id` = \\? and `hash` in").WithArgs("p1", "A").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	resp, _ = ch.processMessage(removeFromPersonReq("p1", []string{"A"}, "", ""))
	if ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck); resp.Error || !ok || !ack.RespAck.Ok {
		t.Errorf("nothing to move: %+v", resp)
	}

	// A face given to no one: the person, left with none, is read again
	// for the matching set and has no cover to work out.
	mock.ExpectBegin()
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\? and `hash` in").WithArgs("p1", "A", "B").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("f1"))
	mock.ExpectExec("update `faces` set `person_id` = \\?").WithArgs("", "f1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `people` set `cover_face_id` = null").WithArgs("f1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `people_by_hand` where `person_id` = \\? and `hash` in").WithArgs("p1", "A", "B").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("delete from `people` where `id` = \\?").WithArgs("p1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces` where `person_id` = \\?").WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "person_id", "embedding"}))
	resp, _ = ch.processMessage(removeFromPersonReq("p1", []string{"A", "B"}, "", ""))
	if ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck); resp.Error || !ok || !ack.RespAck.Ok {
		t.Errorf("not this person: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func addToPersonReq(person string, hashes []string, lang string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 10, Lang: lang, Payload: &pb.ReqEnvelope_ReqAddToPerson{ReqAddToPerson: &pb.AddToPerson{
		PersonId: person, Hashes: hashes,
	}}}
}

// Info's Add answers with the generic Ack, also when the person was in
// the photos already; what can't be right - face recognition off among it
// - is refused before the database is asked, in the request's language; a
// person gone meanwhile, or photos and videos that are, are said so.
func TestAddToPersonAnswers(t *testing.T) {
	if resp, _ := (&connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}}).processMessage(addToPersonReq("p1", []string{"A"}, "")); resp != nil {
		t.Errorf("before sign-in: %+v", resp)
	}

	off, offMock := photoFacesOwner(t, false)
	ch, mock := photoFacesOwner(t, true)
	tooMany := make([]string, cMaxAddToPerson+1)
	for _, c := range []struct {
		name string
		ch   *connHandler
		req  *pb.ReqEnvelope
		want i18n.Msg
	}{
		{"no person", ch, addToPersonReq("", []string{"A"}, ""), i18n.DevFilesPersonMissing()},
		{"no photos", ch, addToPersonReq("p1", nil, "de"), i18n.DevFilesPersonNoPhotos()},
		{"too many photos", ch, addToPersonReq("p1", tooMany, "fr"), i18n.DevFilesPersonTooMany(cMaxAddToPerson)},
		{"face recognition off", off, addToPersonReq("p1", []string{"A"}, "es"), i18n.DevFilesPersonAddFacesOff()},
	} {
		lang := c.req.Lang
		resp, _ := c.ch.processMessage(c.req)
		if resp == nil || !resp.Error || resp.ErrorMessage != c.want.Render(lang) || resp.Payload != nil {
			t.Errorf("%s: %+v, want %q", c.name, resp, c.want.Render(lang))
		}
	}
	if got := i18n.DevFilesPersonAddFacesOff().Render("es"); got == i18n.DevFilesPersonAddFacesOff().Render("") {
		t.Errorf("the refusal isn't in Spanish: %q", got)
	}
	if err := offMock.ExpectationsWereMet(); err != nil {
		t.Error(err) // nothing asked of the database
	}

	person := func(id string, there bool) {
		rows := sqlmock.NewRows([]string{"1"})
		if there {
			rows.AddRow(1)
		}
		mock.ExpectBegin()
		mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs(id).WillReturnRows(rows)
	}
	isAck := func(resp *pb.RespEnvelope) bool {
		ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck)
		return resp != nil && !resp.Error && ok && ack.RespAck.Ok
	}

	person("gone", false)
	mock.ExpectRollback()
	if resp, _ := ch.processMessage(addToPersonReq("gone", []string{"A"}, "")); resp == nil || !resp.Error || resp.ErrorMessage != "that person is no longer on this device" {
		t.Errorf("a person who is gone: %+v", resp)
	}

	person("p1", true)
	mock.ExpectQuery("select distinct `f`.`hash`").WithArgs("p1", "Z").WillReturnRows(sqlmock.NewRows([]string{"hash", "free"}))
	mock.ExpectRollback()
	if resp, _ := ch.processMessage(addToPersonReq("p1", []string{"Z"}, "")); resp == nil || !resp.Error || resp.ErrorMessage != "that photo or video is no longer on this device" {
		t.Errorf("photos and videos gone: %+v", resp)
	}

	person("p1", true)
	mock.ExpectQuery("select distinct `f`.`hash`").WithArgs("p1", "A", "V").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "free"}).AddRow("A", true).AddRow("V", true))
	mock.ExpectExec("insert ignore into `people_by_hand` \\(`hash`, `person_id`, `created`\\) values \\(\\?, \\?, \\?\\),\\(\\?, \\?, \\?\\)").
		WithArgs("A", "p1", sqlmock.AnyArg(), "V", "p1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	if resp, _ := ch.processMessage(addToPersonReq("p1", []string{"A", "V"}, "")); !isAck(resp) {
		t.Errorf("added: %+v", resp)
	}

	// There already (a face of theirs, or added before): ok.
	person("p1", true)
	mock.ExpectQuery("select distinct `f`.`hash`").WithArgs("p1", "A").WillReturnRows(sqlmock.NewRows([]string{"hash", "free"}).AddRow("A", false))
	mock.ExpectRollback()
	if resp, _ := ch.processMessage(addToPersonReq("p1", []string{"A"}, "")); !isAck(resp) {
		t.Errorf("there already: %+v", resp)
	}

	person("p1", true)
	mock.ExpectQuery("select distinct `f`.`hash`").WillReturnError(errors.New("database gone"))
	mock.ExpectRollback()
	if resp, _ := ch.processMessage(addToPersonReq("p1", []string{"A"}, "")); resp == nil || !resp.Error || resp.ErrorMessage == "" {
		t.Errorf("a failure: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
