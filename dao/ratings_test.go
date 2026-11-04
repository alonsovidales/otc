// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

var (
	ratingColumn = unknownColumn("rating")
	noSuchTable  = &mysql.MySQLError{Number: 1146, Message: "Table 'otc.photo_ratings' doesn't exist"}
)

func ratingsMock(t *testing.T) (*Dao, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return NewWithDB(db), mock
}

// A database release 163's script hasn't reached lists and reads its
// collections as before, none rated.
func TestImageGroupsWithoutTheRatingColumn(t *testing.T) {
	d, mock := ratingsMock(t)
	mock.ExpectQuery("limit 1\\), `g`\\.`rating` from `image_groups` as `g` order by").WillReturnError(ratingColumn)
	mock.ExpectQuery("limit 1\\) from `image_groups` as `g` order by").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "n", "cover"}).AddRow("g1", "Trip", 3, "h1"))
	gs, err := d.ListImageGroups(nil)
	if err != nil || len(gs) != 1 || gs[0].Rating || gs[0].CoverHash != "h1" {
		t.Fatalf("list: %+v, %v", gs, err)
	}
	mock.ExpectQuery("limit 1\\), `g`\\.`rating` from `image_groups` as `g` where").WithArgs("g1").WillReturnError(ratingColumn)
	mock.ExpectQuery("limit 1\\) from `image_groups` as `g` where").WithArgs("g1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "n", "cover"}).AddRow("g1", "Trip", 3, "h1"))
	if g, err := d.GetImageGroup("g1", nil); err != nil || g.Rating || g.FileCount != 3 {
		t.Fatalf("get: %+v, %v", g, err)
	}
	mock.ExpectQuery("select `rating` from `image_groups` where `id` = \\?").WithArgs("g1").WillReturnError(ratingColumn)
	mock.ExpectQuery("select 1 from `image_groups` where `id` = \\?").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	if exists, on, err := d.ImageGroupRating("g1"); err != nil || !exists || on {
		t.Fatalf("rating: %v %v %v", exists, on, err)
	}
	mock.ExpectQuery("select `rating` from `image_groups`").WithArgs("g2").WillReturnRows(sqlmock.NewRows([]string{"rating"}))
	if err := d.SetImageGroupRating("g2", true); !errors.Is(err, ErrNoSuchGroup) {
		t.Fatalf("a collection that isn't there: %v", err)
	}
	mock.ExpectQuery("select `rating` from `image_groups`").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"rating"}).AddRow(false))
	mock.ExpectExec("update `image_groups` set `rating` = \\? where `id` = \\?").WithArgs(true, "g1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.SetImageGroupRating("g1", true); err != nil {
		t.Fatal(err)
	}
}

// The rating filter and order join the collection's photo_ratings rows to
// its members, ranked by stars like a tag search by score; numbers only,
// so the placeholders stay the other filters'.
func TestSearchMediaRatedClauses(t *testing.T) {
	before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	plainFrom, plainWhere, plainOrder, _, plainArgs := searchMediaClauses("", []string{"dog"}, nil, "g1", true, &before, nil, nil)
	for _, q := range []*RatingQuery{nil, {}, {MinStars: 0}} {
		from, where, order, _, args := searchMediaRatedClauses("", []string{"dog"}, nil, "g1", true, &before, nil, nil, q)
		if from != plainFrom || where != plainWhere || order != plainOrder || len(args) != len(plainArgs) {
			t.Errorf("%+v changed the search: %s %s %s", q, from, where, order)
		}
	}
	// No collection, nothing joined.
	from, _, _, _, _ := searchMediaRatedClauses("", nil, nil, "", true, nil, nil, nil, &RatingQuery{MinStars: 4})
	if strings.Contains(from, "photo_ratings") {
		t.Errorf("rated without a collection: %s", from)
	}

	from, where, order, _, args := searchMediaRatedClauses("", nil, nil, "g1", true, &before, nil, nil, &RatingQuery{MinStars: 4, WithComment: true})
	if !strings.Contains(from, "join `image_group_files` as `ig` on `ig`.`hash` = `f`.`hash` and `ig`.`group_id` = ? left join `photo_ratings` as `pr` on `pr`.`group_id` = `ig`.`group_id` and `pr`.`hash` = `f`.`hash`") {
		t.Errorf("from: %s", from)
	}
	if !strings.Contains(where, "coalesce(`pr`.`stars`, 0) >= 4") || !strings.Contains(where, "`pr`.`comment` is not null") {
		t.Errorf("where: %s", where)
	}
	if order != " order by `f`.`created` desc, `f`.`path` desc" {
		t.Errorf("a filter alone keeps the date order: %s", order)
	}
	if len(args) != 2 || args[0] != "g1" {
		t.Errorf("args %v", args)
	}

	_, where, _, _, _ = searchMediaRatedClauses("", nil, nil, "g1", true, nil, nil, nil, &RatingQuery{MinStars: 9, Unrated: true})
	if !strings.Contains(where, "coalesce(`pr`.`stars`, 0) = 0") || strings.Contains(where, ">=") {
		t.Errorf("unrated wins over min stars: %s", where)
	}
	_, where, _, _, _ = searchMediaRatedClauses("", nil, nil, "g1", true, nil, nil, nil, &RatingQuery{MinStars: 9})
	if !strings.Contains(where, ">= 5") {
		t.Errorf("min stars past 5: %s", where)
	}

	_, _, order, _, _ = searchMediaRatedClauses("", nil, nil, "g1", true, nil, nil, nil, &RatingQuery{ByRating: true})
	if order != " order by coalesce(`pr`.`stars`, 0) desc, `f`.`created` desc, `f`.`path` desc" {
		t.Errorf("by rating: %s", order)
	}
	_, _, order, _, _ = searchMediaRatedClauses("", []string{"dog"}, nil, "g1", true, nil, nil, nil, &RatingQuery{ByRating: true})
	if order != " order by coalesce(`pr`.`stars`, 0) desc, `tg`.`score` desc, `f`.`created` desc, `f`.`path` desc" {
		t.Errorf("by rating with a tag: %s", order)
	}
}

// Ratings go with their photo when it leaves the collection, and with the
// collection; a database without the table removes as before.
func TestRatingsGoWithTheirMembership(t *testing.T) {
	d, mock := ratingsMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("delete from `image_group_files` where `group_id` = \\? and `hash` in \\(select `hash` from `files` where `path` in \\(\\?,\\?\\)\\)").
		WithArgs("g1", "/a.jpg", "/b.jpg").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("delete from `photo_ratings` where `group_id` = \\? and `hash` in \\(select `hash` from `files` where `path` in \\(\\?,\\?\\)\\)").
		WithArgs("g1", "/a.jpg", "/b.jpg").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `photo_rating_sources` where `group_id` = \\? and `hash` in \\(select `hash` from `files` where `path` in \\(\\?,\\?\\)\\)").
		WithArgs("g1", "/a.jpg", "/b.jpg").WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit()
	if err := d.RemovePathsFromImageGroup("g1", []string{"/a.jpg", "/b.jpg"}); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("delete from `image_group_files`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `photo_ratings`").WillReturnError(noSuchTable)
	mock.ExpectExec("delete from `photo_rating_sources`").WillReturnError(noSuchTable)
	mock.ExpectCommit()
	if err := d.RemovePathsFromImageGroup("g1", []string{"/a.jpg"}); err != nil {
		t.Fatalf("without the tables: %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("delete from `image_group_files` where `group_id` = \\?").WithArgs("g1").WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("delete from `photo_ratings` where `group_id` = \\?").WithArgs("g1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("delete from `photo_rating_sources` where `group_id` = \\?").WithArgs("g1").WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec("delete from `image_groups` where `id` = \\?").WithArgs("g1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := d.DeleteImageGroup("g1"); err != nil {
		t.Fatal(err)
	}

	// Any other failure is one, and nothing is removed.
	mock.ExpectBegin()
	mock.ExpectExec("delete from `image_group_files`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `photo_ratings`").WillReturnError(errors.New("lost"))
	mock.ExpectRollback()
	if err := d.RemovePathsFromImageGroup("g1", []string{"/a.jpg"}); err == nil {
		t.Fatal("a failure was no error")
	}
}

const (
	upsertSource = "insert into `photo_rating_sources` \\(`group_id`, `hash`, `source`, `stars`, `comment`, `link_comment`, `updated`\\) select `group_id`, `hash`, \\?, \\?, \\?, \\?, \\? from `image_group_files` where `group_id` = \\? and `hash` = \\? on duplicate key update"
	upsertShown  = "insert into `photo_ratings` \\(`group_id`, `hash`, `stars`, `comment`, `shared_comment`, `link`, `updated`\\) select `group_id`, `hash`, \\?, \\?, null, \\?, \\? from `image_group_files` where `group_id` = \\? and `hash` = \\? on duplicate key update"
)

// A rating is its source's own (release 169), written only from the
// photo's membership row, and what the owner sees is the latest change -
// in one transaction. A link's visitors taking theirs back deletes their
// row and shows the owner the latest left; the owner's own clear stays a
// row with neither, hiding the older ones.
func TestSetPhotoRating(t *testing.T) {
	d, mock := ratingsMock(t)
	at := time.Date(2026, 11, 2, 21, 0, 0, 123456000, time.UTC)
	second := at.Truncate(time.Second)

	// A link's rating: its own row (its comment for its visitors too), and
	// the owner sees it, the comment sealed to the owner's key only.
	mock.ExpectBegin()
	mock.ExpectExec(upsertSource).WithArgs("link-1", 4, []byte("sealed"), []byte("shared"), at, "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(upsertShown).WithArgs(4, []byte("sealed"), "link-1", second, "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if stored, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "h1", Stars: 4, Comment: []byte("sealed"), SharedComment: []byte("shared"), Link: "link-1", Updated: at}); err != nil || !stored {
		t.Fatalf("a link's: %v %v", stored, err)
	}
	// The owner's: no copy for anyone.
	mock.ExpectBegin()
	mock.ExpectExec(upsertSource).WithArgs("", 2, []byte("mine"), nil, at, "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(upsertShown).WithArgs(2, []byte("mine"), nil, second, "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	if stored, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "h1", Stars: 2, Comment: []byte("mine"), SharedComment: []byte("stray"), Updated: at}); err != nil || !stored {
		t.Fatalf("the owner's: %v %v", stored, err)
	}
	// Not a member: nothing written.
	mock.ExpectBegin()
	mock.ExpectExec(upsertSource).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("select 1 from `image_group_files` where `group_id` = \\? and `hash` = \\?").WithArgs("g1", "gone").WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectRollback()
	if stored, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "gone", Stars: 2, Updated: at}); err != nil || stored {
		t.Fatalf("not a member: %v %v", stored, err)
	}
	// The owner's clear: kept as a row with neither, and the owner sees
	// none.
	mock.ExpectBegin()
	mock.ExpectExec(upsertSource).WithArgs("", 0, nil, nil, at, "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("delete from `photo_ratings` where `group_id` = \\? and `hash` = \\?").WithArgs("g1", "h1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if stored, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "h1", Updated: at}); err != nil || !stored {
		t.Fatalf("the owner's clear: %v %v", stored, err)
	}
	// A link's taken back, while the owner saw it: the latest left shows.
	mock.ExpectBegin()
	mock.ExpectExec("delete from `photo_rating_sources` where `group_id` = \\? and `hash` = \\? and `source` = \\?").WithArgs("g1", "h1", "link-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `link` from `photo_ratings` where `group_id` = \\? and `hash` = \\? for update").WithArgs("g1", "h1").WillReturnRows(sqlmock.NewRows([]string{"link"}).AddRow("link-1"))
	mock.ExpectQuery("select `source`, `stars`, `comment`, `updated` from `photo_rating_sources` where `group_id` = \\? and `hash` = \\? order by `updated` desc limit 1").
		WithArgs("g1", "h1").WillReturnRows(sqlmock.NewRows([]string{"source", "stars", "comment", "updated"}).AddRow("link-2", 3, nil, at))
	mock.ExpectExec(upsertShown).WithArgs(3, nil, "link-2", second, "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	if stored, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "h1", Link: "link-1", Updated: at}); err != nil || !stored {
		t.Fatalf("taken back: %v %v", stored, err)
	}
	// ... the latest left the owner's clear: the owner sees none.
	mock.ExpectBegin()
	mock.ExpectExec("delete from `photo_rating_sources`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `link` from `photo_ratings`").WillReturnRows(sqlmock.NewRows([]string{"link"}).AddRow("link-2"))
	mock.ExpectQuery("select `source`, `stars`, `comment`, `updated` from `photo_rating_sources`").WillReturnRows(sqlmock.NewRows([]string{"source", "stars", "comment", "updated"}).AddRow("", 0, nil, at))
	mock.ExpectExec("delete from `photo_ratings` where `group_id` = \\? and `hash` = \\?").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if _, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "h1", Link: "link-2", Updated: at}); err != nil {
		t.Fatal(err)
	}
	// ... while the owner saw another's: left as it is.
	mock.ExpectBegin()
	mock.ExpectExec("delete from `photo_rating_sources`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `link` from `photo_ratings`").WillReturnRows(sqlmock.NewRows([]string{"link"}).AddRow(nil))
	mock.ExpectCommit()
	if _, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "h1", Link: "link-3", Updated: at}); err != nil {
		t.Fatal(err)
	}
	// A database before release 169.
	mock.ExpectBegin()
	mock.ExpectExec(upsertSource).WillReturnError(&mysql.MySQLError{Number: 1146, Message: "Table 'otc.photo_rating_sources' doesn't exist"})
	mock.ExpectRollback()
	if _, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "h1", Stars: 1}); !errors.Is(err, ErrSchemaBehind) {
		t.Fatalf("schema behind: %v", err)
	}

	mock.ExpectQuery("select `g`.`rating`, exists \\(select 1 from `image_group_files` as `m` where `m`.`group_id` = `g`.`id` and `m`.`hash` = \\?\\) from `image_groups` as `g` where `g`.`id` = \\?").
		WithArgs("h1", "g1").WillReturnRows(sqlmock.NewRows([]string{"rating", "member"}).AddRow(true, false))
	if exists, on, member, err := d.RatingTarget("g1", "h1"); err != nil || !exists || !on || member {
		t.Fatalf("target: %v %v %v %v", exists, on, member, err)
	}
	mock.ExpectQuery("select `g`.`rating`, exists").WillReturnRows(sqlmock.NewRows([]string{"rating", "member"}))
	if exists, _, _, err := d.RatingTarget("g9", "h1"); err != nil || exists {
		t.Fatalf("no collection: %v %v", exists, err)
	}
}

// A page's ratings, by hash; none without the table.
func TestPhotoRatings(t *testing.T) {
	d, mock := ratingsMock(t)
	at := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	mock.ExpectQuery("select `hash`, `stars`, `comment`, `shared_comment`, `link`, `updated` from `photo_ratings` where `group_id` = \\? and `hash` in \\(\\?, \\?\\)").
		WithArgs("g1", "h1", "h2").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "stars", "comment", "shared_comment", "link", "updated"}).
			AddRow("h1", 5, []byte("c"), nil, "link-1", at))
	got, err := d.PhotoRatings("g1", []string{"h1", "h2"})
	if err != nil || len(got) != 1 || got["h1"].Stars != 5 || got["h1"].Link != "link-1" || string(got["h1"].Comment) != "c" || !got["h1"].Updated.Equal(at) {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := d.PhotoRatings("g1", nil); err != nil || len(got) != 0 {
		t.Fatalf("no hashes: %v %v", got, err)
	}
	mock.ExpectQuery("from `photo_ratings`").WillReturnError(noSuchTable)
	if got, err := d.PhotoRatings("g1", []string{"h1"}); err != nil || len(got) != 0 {
		t.Fatalf("no table: %v %v", got, err)
	}
}

// A link's visitors read their link's ratings only; none without the
// table, or for no link.
func TestLinkRatings(t *testing.T) {
	d, mock := ratingsMock(t)
	at := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	mock.ExpectQuery("select `hash`, `stars`, `link_comment`, `updated` from `photo_rating_sources` where `group_id` = \\? and `source` = \\? and `hash` in \\(\\?, \\?\\)").
		WithArgs("g1", "link-1", "h1", "h2").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "stars", "link_comment", "updated"}).AddRow("h1", 5, []byte("theirs"), at))
	got, err := d.LinkRatings("g1", "link-1", []string{"h1", "h2"})
	if err != nil || len(got) != 1 || got["h1"].Stars != 5 || got["h1"].Link != "link-1" || string(got["h1"].SharedComment) != "theirs" {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := d.LinkRatings("g1", "", []string{"h1"}); err != nil || len(got) != 0 {
		t.Fatalf("the owner's own through a link: %v %v", got, err)
	}
	mock.ExpectQuery("from `photo_rating_sources`").WillReturnError(&mysql.MySQLError{Number: 1146, Message: "Table 'otc.photo_rating_sources' doesn't exist"})
	if got, err := d.LinkRatings("g1", "link-1", []string{"h1"}); err != nil || len(got) != 0 {
		t.Fatalf("no table: %v %v", got, err)
	}
}

// At start, every rating of what the owner sees gets its source's row when
// it has none or an older one, and the owner's own comments' copies for
// links go; a database before release 169 is left alone.
func TestReconcileRatingSources(t *testing.T) {
	d, mock := ratingsMock(t)
	mock.ExpectExec("insert into `photo_rating_sources` \\(`group_id`, `hash`, `source`, `stars`, `comment`, `link_comment`, `updated`\\) select `pr`.`group_id`, `pr`.`hash`, coalesce\\(`pr`.`link`, ''\\), .* from `photo_ratings` as `pr` on duplicate key update").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("update `photo_ratings` set `shared_comment` = null where `link` is null and `shared_comment` is not null").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.ReconcileRatingSources(); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("insert into `photo_rating_sources`").WillReturnError(&mysql.MySQLError{Number: 1146, Message: "Table 'otc.photo_rating_sources' doesn't exist"})
	if err := d.ReconcileRatingSources(); !errors.Is(err, ErrSchemaBehind) {
		t.Fatalf("no table: %v", err)
	}
}

// A rated gallery records its collection; one without Rating is inserted
// as before.
func TestInsertSharedGalleryRated(t *testing.T) {
	d, mock := ratingsMock(t)
	exp := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec("insert into `shared_links` \\(`uuid`, `size`, `created`, `kind`, `description`, `files`, `expires`\\) values").
		WithArgs("u1", int64(10), sqlmock.AnyArg(), []byte("d"), 2, exp).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.InsertSharedGallery("u1", 10, 2, []byte("d"), exp, ""); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("insert into `shared_links` \\(`uuid`, `size`, `created`, `kind`, `description`, `files`, `expires`, `group_id`, `rating`\\) values \\(\\?, \\?, \\?, 'gallery', \\?, \\?, \\?, \\?, 1\\)").
		WithArgs("u2", int64(10), sqlmock.AnyArg(), []byte("d"), 2, exp, "g1").WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.InsertSharedGallery("u2", 10, 2, []byte("d"), exp, "g1"); err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery("select `group_id`, `rating` from `shared_links` where `uuid` = \\?").WithArgs("u2").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "rating"}).AddRow("g1", true))
	if g, r, err := d.GetSharedLinkRating("u2"); err != nil || g != "g1" || !r {
		t.Fatalf("%s %v %v", g, r, err)
	}
	mock.ExpectQuery("select `group_id`, `rating` from `shared_links`").WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "rating"}).AddRow(nil, false))
	if g, r, err := d.GetSharedLinkRating("u1"); err != nil || g != "" || r {
		t.Fatalf("unrated: %s %v %v", g, r, err)
	}
	mock.ExpectQuery("select `group_id`, `rating` from `shared_links`").WillReturnError(ratingColumn)
	if g, r, err := d.GetSharedLinkRating("u1"); err != nil || g != "" || r {
		t.Fatalf("schema behind: %s %v %v", g, r, err)
	}
}

// Settings' list says which links take ratings now: made with Rating, and
// their collection there with its Rating on.
func TestListSharedLinksRating(t *testing.T) {
	d, mock := ratingsMock(t)
	now := time.Now()
	cols := []string{"uuid", "kind", "description", "created", "expires", "opens", "last_opened", "size", "files"}
	mock.ExpectQuery("`l`.`rating` = 1 and coalesce\\(`g`.`rating`, 0\\) = 1 from `shared_links` as `l` left join `image_groups` as `g` on `g`.`id` = `l`.`group_id` order by `l`.`created` desc").
		WillReturnRows(sqlmock.NewRows(append(cols, "rating")).
			AddRow("u1", "gallery", nil, now, nil, 0, nil, 10, 1, true).
			AddRow("u2", "archive", nil, now, nil, 0, nil, 10, 1, false))
	rows, err := d.ListSharedLinks()
	if err != nil || len(rows) != 2 || !rows[0].Rating || rows[1].Rating {
		t.Fatalf("%+v %v", rows, err)
	}
	mock.ExpectQuery("from `shared_links` as `l` left join").WillReturnError(ratingColumn)
	mock.ExpectQuery("from `shared_links` as `l` order by `l`.`created` desc").
		WillReturnRows(sqlmock.NewRows(cols).AddRow("u1", "gallery", nil, now, nil, 0, nil, 10, 1))
	if rows, err := d.ListSharedLinks(); err != nil || len(rows) != 1 || rows[0].Rating {
		t.Fatalf("schema behind: %+v %v", rows, err)
	}
}

// At most one alert per link per hour: the update that records it is the
// claim, and only the one that changed the row may alert.
func TestClaimRatingAlert(t *testing.T) {
	d, mock := ratingsMock(t)
	now := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	mock.ExpectExec("update `shared_links` set `rating_alerted` = \\? where `uuid` = \\? and \\(`rating_alerted` is null or `rating_alerted` <= \\?\\)").
		WithArgs(now, "u1", now.Add(-time.Hour)).WillReturnResult(sqlmock.NewResult(0, 1))
	if ok, err := d.ClaimRatingAlert("u1", time.Hour, now); err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	mock.ExpectExec("update `shared_links` set `rating_alerted`").WillReturnResult(sqlmock.NewResult(0, 0))
	if ok, err := d.ClaimRatingAlert("u1", time.Hour, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("within the hour: %v %v", ok, err)
	}
}

// The key pair: read, made only where there is none, and a database
// before release 163 says so.
func TestRatingKeys(t *testing.T) {
	d, mock := ratingsMock(t)
	mock.ExpectQuery("select `rating_public`, `rating_private` from `vault` limit 1").
		WillReturnRows(sqlmock.NewRows([]string{"rating_public", "rating_private"}).AddRow(nil, nil))
	if pub, priv, err := d.RatingKeys(); err != nil || pub != nil || priv != nil {
		t.Fatalf("none: %v %v %v", pub, priv, err)
	}
	mock.ExpectExec("update `vault` set `rating_public` = \\?, `rating_private` = \\? where `rating_public` is null").
		WithArgs([]byte("pub"), []byte("priv")).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.SetRatingKeysIfAbsent([]byte("pub"), []byte("priv")); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("select `rating_public`, `rating_private` from `vault`").WillReturnError(ratingColumn)
	if _, _, err := d.RatingKeys(); !errors.Is(err, ErrSchemaBehind) {
		t.Fatalf("schema behind: %v", err)
	}

	mock.ExpectExec("update `image_groups` set `rating_key` = \\? where `id` = \\? and `rating_key` is null").
		WithArgs([]byte("k2"), "g1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("select `rating_key` from `image_groups` where `id` = \\?").WithArgs("g1").
		WillReturnRows(sqlmock.NewRows([]string{"rating_key"}).AddRow([]byte("k1")))
	if key, err := d.SetImageGroupRatingKeyIfAbsent("g1", []byte("k2")); err != nil || string(key) != "k1" {
		t.Fatalf("the first key stays: %q %v", key, err)
	}
	mock.ExpectQuery("select `rating_key` from `image_groups`").WithArgs("g9").WillReturnRows(sqlmock.NewRows([]string{"rating_key"}))
	if _, err := d.ImageGroupRatingKey("g9"); !errors.Is(err, ErrNoSuchGroup) {
		t.Fatalf("no collection: %v", err)
	}
}
