// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func setGroupRatingReq(id string, on bool) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 9, Lang: "es", Payload: &pb.ReqEnvelope_ReqSetImageGroupRating{ReqSetImageGroupRating: &pb.SetImageGroupRating{Id: id, On: on}}}
}

func setPhotoRatingReq(group, path string, stars int32, comment string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 9, Lang: "es", Payload: &pb.ReqEnvelope_ReqSetPhotoRating{ReqSetPhotoRating: &pb.SetPhotoRating{GroupId: group, Path: path, Stars: stars, Comment: comment}}}
}

func rateItemReq(uuid string, stars int32) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 9, Lang: "es", Payload: &pb.ReqEnvelope_ReqRateSharedGalleryItem{ReqRateSharedGalleryItem: &pb.RateSharedGalleryItem{Uuid: uuid, Secret: strings.Repeat("ab", 32), Stars: stars}}}
}

// The owner's two requests: a collection's Rating on or off (Ack), a
// photo's rating (PhotoRating) - refused with their codes and words, and
// never before sign-in.
func TestRatingOwnerRequests(t *testing.T) {
	anon := &connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}}
	for _, req := range []*pb.ReqEnvelope{setGroupRatingReq("g1", true), setPhotoRatingReq("g1", "/a.jpg", 3, "")} {
		if resp, _ := anon.processMessage(req); resp != nil {
			t.Errorf("%T before sign-in: %+v", req.Payload, resp)
		}
	}

	ch, mock := ownerWithLibrary(t)
	mock.ExpectQuery("select `rating` from `image_groups` where `id` = \\?").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"rating"}).AddRow(false))
	mock.ExpectExec("update `image_groups` set `rating` = \\? where `id` = \\?").WithArgs(true, "g1").WillReturnResult(sqlmock.NewResult(0, 1))
	resp, _ := ch.processMessage(setGroupRatingReq("g1", true))
	if ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck); resp.Error || !ok || !ack.RespAck.Ok {
		t.Errorf("on: %+v", resp)
	}
	mock.ExpectQuery("select `rating` from `image_groups` where `id` = \\?").WithArgs("gone").WillReturnRows(sqlmock.NewRows([]string{"rating"}))
	resp, _ = ch.processMessage(setGroupRatingReq("gone", false))
	if ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck); !resp.Error || !ok || ack.RespAck.Ok || resp.ErrorMessage != i18n.DevFilesRatingNoCollection().Render("es") {
		t.Errorf("a collection that is gone: %+v", resp)
	}

	target := func(on, member bool) {
		mock.ExpectQuery("from `files` where `path` = \\?").WithArgs("/a.jpg").
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow("h1", "image/jpeg", time.Now(), time.Now(), "/a.jpg", 1))
		mock.ExpectQuery("select `g`.`rating`, exists").WithArgs("h1", "g1").WillReturnRows(sqlmock.NewRows([]string{"rating", "member"}).AddRow(on, member))
	}
	target(true, true)
	mock.ExpectBegin()
	mock.ExpectExec("insert into `photo_rating_sources`").WithArgs("", 4, nil, nil, sqlmock.AnyArg(), "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("insert into `photo_ratings`").WithArgs(4, nil, nil, sqlmock.AnyArg(), "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	resp, _ = ch.processMessage(setPhotoRatingReq("g1", "/a.jpg", 4, ""))
	if got, ok := resp.GetPayload().(*pb.RespEnvelope_RespPhotoRating); resp.Error || !ok || got.RespPhotoRating.Stars != 4 || got.RespPhotoRating.FromLink {
		t.Errorf("rated: %+v", resp)
	}
	for _, c := range []struct {
		on, member bool
		code       string
		msg        i18n.Msg
	}{{false, true, "rating_off", i18n.DevFilesRatingOff()}, {true, false, "not_in_collection", i18n.DevFilesRatingNotInCollection()}} {
		target(c.on, c.member)
		resp, _ = ch.processMessage(setPhotoRatingReq("g1", "/a.jpg", 4, ""))
		if !resp.Error || resp.ErrorCode != c.code || resp.ErrorMessage != c.msg.Render("es") || resp.Payload != nil {
			t.Errorf("%s: %+v", c.code, resp)
		}
	}
	resp, _ = ch.processMessage(setPhotoRatingReq("g1", "/a.jpg", 7, ""))
	if !resp.Error || resp.ErrorCode != "" || resp.ErrorMessage != i18n.DevFilesRatingBadStars().Render("es") {
		t.Errorf("7 stars: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A visitor's rating is public: a link that doesn't open gets the shared
// gallery's one answer, and each address is held to its share.
func TestRateSharedGalleryItemIsPublicAndLimited(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	orig := visitorRatings
	visitorRatings = newRatingLimiter()
	visitorRatings.now = func() time.Time { return now }
	t.Cleanup(func() { visitorRatings = orig })

	ch := &connHandler{mg: &Manager{filesManager: &filesmanager.Manager{}}, remoteAddr: "203.0.113.7"}
	notFound := i18n.DevFilesGalleryNotFound().Render("es")
	for i := range cRatingBurst {
		resp, _ := ch.processMessage(rateItemReq("not-a-link", 3))
		if resp == nil || !resp.Error || resp.ErrorMessage != notFound || resp.ErrorCode != "" || resp.Payload != nil {
			t.Fatalf("%d: %+v", i, resp)
		}
	}
	resp, _ := ch.processMessage(rateItemReq("not-a-link", 3))
	ack, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck)
	if !resp.Error || resp.ErrorCode != "too_many_attempts" || !ok || ack.RespAck.Ok || ack.RespAck.RetryAfterSeconds != 1 {
		t.Fatalf("past the burst: %+v", resp)
	}
	// Another address has its own share; a second later this one has one more.
	other := &connHandler{mg: ch.mg, remoteAddr: "198.51.100.9"}
	if resp, _ := other.processMessage(rateItemReq("not-a-link", 3)); resp.ErrorCode != "" {
		t.Errorf("another address: %+v", resp)
	}
	now = now.Add(time.Second)
	if resp, _ := ch.processMessage(rateItemReq("not-a-link", 3)); resp.ErrorCode != "" {
		t.Errorf("a second later: %+v", resp)
	}
}

// The device takes at most its own share of visitors' ratings, whatever
// the addresses; idle addresses are forgotten.
func TestRatingLimiterDeviceShare(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := newRatingLimiter()
	l.now = func() time.Time { return now }
	for i := range cRatingDeviceBurst {
		if _, ok := l.allow(string(rune('a'+i%26)) + string(rune('a'+i/26))); !ok {
			t.Fatalf("refused at %d", i)
		}
	}
	if wait, ok := l.allow("new"); ok || wait <= 0 {
		t.Fatalf("past the device's share: %v %v", wait, ok)
	}
	if _, ok := l.by["new"]; ok {
		t.Error("an address refused by the device's share was kept")
	}
	now = now.Add(time.Hour)
	l.allow("late")
	if len(l.by) != 1 {
		t.Errorf("%d addresses kept after an idle hour, want 1", len(l.by))
	}
}

func TestRatingQuery(t *testing.T) {
	if q := ratingQuery(&pb.SearchPhotos{}); q != nil {
		t.Errorf("none: %+v", q)
	}
	if q := ratingQuery(&pb.SearchPhotos{Rating: &pb.RatingFilter{}}); q != nil {
		t.Errorf("an empty filter: %+v", q)
	}
	q := ratingQuery(&pb.SearchPhotos{Rating: &pb.RatingFilter{MinStars: 12, WithComment: true}, ByRating: true})
	if q == nil || q.MinStars != 5 || !q.WithComment || !q.ByRating || q.Unrated {
		t.Errorf("%+v", q)
	}
	if q := ratingQuery(&pb.SearchPhotos{Rating: &pb.RatingFilter{MinStars: -3, Unrated: true}}); q == nil || q.MinStars != 0 || !q.Unrated {
		t.Errorf("unrated: %+v", q)
	}
}

// keyArg matches an alert's stored key.
type keyArg string

func (k keyArg) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok {
		if ns, isNull := v.(interface{ Value() (driver.Value, error) }); isNull {
			got, _ := ns.Value()
			s, ok = got.(string)
		}
	}
	return ok && s == string(k)
}

// Ratings through a link alert the owner: an alert of its own, of type
// Rating, naming the collection (Favourites by its key) and carrying its
// title as actor_name for the apps from before the type, and a push that
// names none.
func TestRatingAlert(t *testing.T) {
	a := ratingAlert(&filesmanager.RatingNotice{GroupName: "Wedding"})
	if a.Key != "dev.alert.rating.new" || a.Title != "Photos rated through a shared link" || !strings.Contains(a.Detail, "Wedding") {
		t.Errorf("%+v", a)
	}
	fav := ratingAlert(&filesmanager.RatingNotice{GroupName: "Favourites", Favourites: true})
	if !strings.Contains(fav.Detail, "in Favourites through") {
		t.Errorf("favourites: %+v", fav)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}
	mock.ExpectExec("insert into `notifications` \\(`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`, `msg_key`, `msg_args`, `msg_lines`, `group_id`\\) values \\(\\?, now\\(\\), 'Rating', \\?, '', \\?, \\?, 1, \\?, \\?, \\?, \\?\\)").
		WithArgs(sqlmock.AnyArg(), "Photos rated through a shared link", "Photos rated through a shared link", sqlmock.AnyArg(), keyArg("dev.alert.rating.new"), sqlmock.AnyArg(), sqlmock.AnyArg(), "g1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mg.notifyRatings(&filesmanager.RatingNotice{GroupID: "g1", GroupName: "Wedding"})
	mg.notifyRatings(nil)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
