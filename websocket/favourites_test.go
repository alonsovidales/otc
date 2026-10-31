// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func getFavouritesReq(paths ...string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 9, Payload: &pb.ReqEnvelope_ReqGetFavourites{ReqGetFavourites: &pb.GetFavourites{Paths: paths}}}
}

func setFavouriteReq(favourite bool, paths ...string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 9, Payload: &pb.ReqEnvelope_ReqSetFavourite{ReqSetFavourite: &pb.SetFavourite{Paths: paths, Favourite: favourite}}}
}

// The viewer's heart: GetFavourites answers with the paths whose photo is a
// favourite, SetFavourite puts photos in (making Favourites first if it
// isn't there yet) or takes them out, with the generic Ack.
func TestFavouritesRequests(t *testing.T) {
	if resp, _ := (&connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}}).processMessage(getFavouritesReq("/a.jpg")); resp != nil {
		t.Errorf("before sign-in: %+v", resp)
	}

	ch, mock := ownerWithLibrary(t)
	mock.ExpectQuery("select `f`.`path` from `files` as `f` join `image_group_files` as `m` on `m`.`hash` = `f`.`hash` and `m`.`group_id` = \\? where `f`.`path` in \\(\\?,\\?\\)").
		WithArgs("favourites", "/a.jpg", "/b.jpg").
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/a.jpg"))
	resp, _ := ch.processMessage(getFavouritesReq("/a.jpg", "/b.jpg"))
	if got, ok := resp.GetPayload().(*pb.RespEnvelope_RespFavourites); resp.Error || !ok ||
		len(got.RespFavourites.Paths) != 1 || got.RespFavourites.Paths[0] != "/a.jpg" {
		t.Errorf("get: %+v", resp)
	}

	mock.ExpectExec("insert ignore into `image_groups` \\(`id`, `name`, `created`\\) values \\(\\?, 'Favourites', \\?\\)").
		WithArgs("favourites", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("insert ignore into `image_group_files`").
		WithArgs("favourites", sqlmock.AnyArg(), "/b.jpg").WillReturnResult(sqlmock.NewResult(0, 1))
	resp, _ = ch.processMessage(setFavouriteReq(true, "/b.jpg"))
	if ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck); resp.Error || !ok || !ack.RespAck.Ok {
		t.Errorf("set: %+v", resp)
	}

	mock.ExpectExec("delete from `image_group_files` where `group_id` = \\?").
		WithArgs("favourites", "/a.jpg").WillReturnResult(sqlmock.NewResult(0, 1))
	resp, _ = ch.processMessage(setFavouriteReq(false, "/a.jpg"))
	if ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck); resp.Error || !ok || !ack.RespAck.Ok {
		t.Errorf("unset: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Favourites is always there: it can't be renamed or deleted, and it is
// listed under its name in the request's language.
func TestFavouritesStays(t *testing.T) {
	ch, mock := ownerWithLibrary(t)
	for _, req := range []*pb.ReqEnvelope{
		{Id: 9, Lang: "es", Payload: &pb.ReqEnvelope_ReqRenameImageGroup{ReqRenameImageGroup: &pb.RenameImageGroup{Id: "favourites", Name: "Mine"}}},
		{Id: 9, Lang: "es", Payload: &pb.ReqEnvelope_ReqDeleteImageGroup{ReqDeleteImageGroup: &pb.DeleteImageGroup{Id: "favourites"}}},
	} {
		resp, _ := ch.processMessage(req)
		if resp == nil || !resp.Error || resp.ErrorMessage != i18n.DevFilesFavouritesFixed().Render("es") {
			t.Errorf("%T: %+v", req.Payload, resp)
		}
	}

	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	mock.ExpectExec("insert ignore into `image_groups`").WithArgs("favourites", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("from `image_groups` as `g` order by `g`.`id` = 'favourites' desc").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "n", "cover"}).
			AddRow("favourites", "Favourites", 0, nil).AddRow("g1", "Trip", 2, nil))
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 9, Lang: "es", Payload: &pb.ReqEnvelope_ReqListImageGroups{ReqListImageGroups: &pb.ListImageGroups{}}})
	got, ok := resp.GetPayload().(*pb.RespEnvelope_RespImageGroups)
	if resp.Error || !ok || len(got.RespImageGroups.Groups) != 2 {
		t.Fatalf("list: %+v", resp)
	}
	fav, trip := got.RespImageGroups.Groups[0], got.RespImageGroups.Groups[1]
	if !fav.Favourites || fav.Name != "Favoritos" || trip.Favourites || trip.Name != "Trip" {
		t.Errorf("list: %+v, %+v", fav, trip)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
