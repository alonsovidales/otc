// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

// Issue #173: the batched queries that replaced per-post / per-tag /
// per-file round trips.

import (
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A feed page of 3 posts from 2 friends reads each friend's profile once,
// the files of the whole page in one query and the liked flags in one
// more - not three queries per post.
func TestGetSocialPublicationsBatchesPerPageQueries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("select `friend_domain`, `uuid`, `dt`, `text`, `own_publication`, `likes` from `social_publications` where uuid not in \\(\\?\\)  order by `dt` desc limit \\?").
		WithArgs("", int32(3)).
		WillReturnRows(sqlmock.NewRows([]string{"friend_domain", "uuid", "dt", "text", "own_publication", "likes"}).
			AddRow("a.off-the.cloud", "p1", now, "one", false, 1).
			AddRow("b.off-the.cloud", "p2", now.Add(-time.Minute), "two", false, 0).
			AddRow("a.off-the.cloud", "p3", now.Add(-2*time.Minute), "three", false, 4))
	friendRow := func(name string) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"status", "name", "image", "text", "sent"}).
			AddRow("accepted", name, []byte("img-"+name), "bio "+name, false)
	}
	mock.ExpectQuery("select `status`, `name`, `image`, `text`, `sent` from `social_friendship` where `domain` = \\?").
		WithArgs("a.off-the.cloud").WillReturnRows(friendRow("A"))
	mock.ExpectQuery("select `status`, `name`, `image`, `text`, `sent` from `social_friendship` where `domain` = \\?").
		WithArgs("b.off-the.cloud").WillReturnRows(friendRow("B"))
	mock.ExpectQuery("select `uuid`, `hash`, `mime`, `created`, `modified`, `size` from `social_publications_files` where `uuid` in \\(\\?,\\?,\\?\\) order by `pos`").
		WithArgs("p1", "p2", "p3").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "hash", "mime", "created", "modified", "size"}).
			AddRow("p3", "h3a", "image/jpeg", now, now, 10).
			AddRow("p1", "h1a", "image/jpeg", now, now, 11).
			AddRow("p3", "h3b", "video/mp4", now, now, 12))
	mock.ExpectQuery("select distinct `pub_uuid` from `social_publication_likes` where `friend_domain` = \\? and `pub_uuid` in \\(\\?,\\?,\\?\\)").
		WithArgs("me.off-the.cloud", "p1", "p2", "p3").
		WillReturnRows(sqlmock.NewRows([]string{"pub_uuid"}).AddRow("p3"))

	d := NewWithDB(db)
	pubs, err := d.GetSocialPublications(now, 3, false, nil, "Me", "my bio", nil, "me.off-the.cloud")
	if err != nil {
		t.Fatalf("GetSocialPublications: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("not all expected queries ran: %v", err)
	}

	if len(pubs.Publications) != 3 {
		t.Fatalf("want 3 publications, got %d", len(pubs.Publications))
	}
	p1, p2, p3 := pubs.Publications[0], pubs.Publications[1], pubs.Publications[2]
	if p1.Uuid != "p1" || p2.Uuid != "p2" || p3.Uuid != "p3" {
		t.Fatalf("order changed: %s %s %s", p1.Uuid, p2.Uuid, p3.Uuid)
	}
	if p1.Publisher.Name != "A" || p3.Publisher.Name != "A" || p2.Publisher.Name != "B" ||
		p2.Publisher.Domain != "b.off-the.cloud" || string(p1.Publisher.Image) != "img-A" ||
		string(p3.Publisher.Image) != "img-A" || p1.Publisher.Text != "bio A" {
		t.Fatalf("wrong publishers: %+v %+v %+v", p1.Publisher, p2.Publisher, p3.Publisher)
	}
	if len(p1.Files) != 1 || p1.Files[0].Hash != "h1a" || p2.Files != nil ||
		len(p3.Files) != 2 || p3.Files[0].Hash != "h3a" || p3.Files[1].Hash != "h3b" {
		t.Fatalf("files not grouped per post in pos order: %v %v %v", p1.Files, p2.Files, p3.Files)
	}
	if p1.Liked || p2.Liked || !p3.Liked {
		t.Fatalf("wrong liked flags: %v %v %v", p1.Liked, p2.Liked, p3.Liked)
	}
	if p1.Likes != 1 || p3.Likes != 4 || p1.Own {
		t.Fatalf("wrong row fields: %+v %+v", p1, p3)
	}
	if !pubs.Since.AsTime().Equal(now.Add(-2 * time.Minute)) {
		t.Fatalf("Since should be the last post's time, got %v", pubs.Since.AsTime())
	}
}

// A friend whose profile can't be read still has their posts skipped (as
// the per-post lookup did), and is looked up once for the page.
func TestGetSocialPublicationsSkipsUnknownFriendOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("select `friend_domain`, `uuid`, `dt`, `text`, `own_publication`, `likes` from `social_publications`").
		WillReturnRows(sqlmock.NewRows([]string{"friend_domain", "uuid", "dt", "text", "own_publication", "likes"}).
			AddRow("gone.off-the.cloud", "p1", now, "one", false, 0).
			AddRow("", "p2", now.Add(-time.Minute), "mine", true, 0).
			AddRow("gone.off-the.cloud", "p3", now.Add(-2*time.Minute), "three", false, 0))
	mock.ExpectQuery("select `status`, `name`, `image`, `text`, `sent` from `social_friendship` where `domain` = \\?").
		WithArgs("gone.off-the.cloud").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("from `social_publications_files` where `uuid` in").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "hash", "mime", "created", "modified", "size"}))
	mock.ExpectQuery("from `social_publication_likes` where `friend_domain` = \\? and `pub_uuid` in").
		WillReturnRows(sqlmock.NewRows([]string{"pub_uuid"}))

	d := NewWithDB(db)
	pubs, err := d.GetSocialPublications(now, 10, false, nil, "Me", "", []byte("me"), "me.off-the.cloud")
	if err != nil {
		t.Fatalf("GetSocialPublications: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("not all expected queries ran: %v", err)
	}
	if len(pubs.Publications) != 1 || pubs.Publications[0].Uuid != "p2" || pubs.Publications[0].Publisher.Name != "Me" {
		t.Fatalf("want only the own post, got %+v", pubs.Publications)
	}
}

// An empty page runs no follow-up queries at all.
func TestGetSocialPublicationsEmptyPage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("from `social_publications`").
		WillReturnRows(sqlmock.NewRows([]string{"friend_domain", "uuid", "dt", "text", "own_publication", "likes"}))

	pubs, err := NewWithDB(db).GetSocialPublications(time.Now(), 10, true, []string{"x"}, "Me", "", nil, "me")
	if err != nil || len(pubs.Publications) != 0 || pubs.Since != nil {
		t.Fatalf("got %+v, %v", pubs, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// All of a file's tags go in one upsert.
func TestAddTagsSingleMultiRowUpsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("insert into `file_tags` \\(`hash`, `tag`, `score`\\) values \\(\\?, \\?, \\?\\), \\(\\?, \\?, \\?\\), \\(\\?, \\?, \\?\\) on duplicate key update `score` = values\\(`score`\\)").
		WithArgs("h", "cat", float32(0.9), "h", "sofa", float32(0.5), "h", "Madrid", float32(1)).
		WillReturnResult(sqlmock.NewResult(0, 3))

	d := NewWithDB(db)
	d.AddTags(&pb.File{Hash: "h"}, []imagestagger.RAMTag{{Name: "cat", Score: 0.9}, {Name: "sofa", Score: 0.5}, {Name: "Madrid", Score: 1}})
	d.AddTags(&pb.File{Hash: "h"}, nil) // no tags, no statement
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// If the batch fails, each tag is still tried on its own, so one bad tag
// doesn't cost the file the others (as with the old per-tag loop).
func TestAddTagsFallsBackPerTag(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	const single = "insert into `file_tags` \\(`hash`, `tag`, `score`\\) values \\(\\?, \\?, \\?\\) on duplicate key update `score` = values\\(`score`\\)"
	mock.ExpectExec("insert into `file_tags` .* values \\(\\?, \\?, \\?\\), \\(\\?, \\?, \\?\\) on duplicate").
		WillReturnError(sql.ErrConnDone)
	mock.ExpectExec(single).WithArgs("h", "bad", float32(1)).WillReturnError(sql.ErrConnDone)
	mock.ExpectExec(single).WithArgs("h", "good", float32(1)).WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	d.AddTags(&pb.File{Hash: "h"}, []imagestagger.RAMTag{{Name: "bad", Score: 1}, {Name: "good", Score: 1}})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// DelFileByPathHash hands back the deleted row's hash, and sql.ErrNoRows
// for a missing path (DelFileByPath still treats that as nothing to do).
func TestDelFileByPathHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\?").
		WithArgs("/a.jpg").WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("abc"))
	mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").
		WithArgs("abc").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(2))
	mock.ExpectQuery("select count\\(\\*\\) from `file_versions` where `hash` = \\?").
		WithArgs("abc").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("delete from `files` where `path` = \\?").
		WithArgs("/a.jpg").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if hash, err := d.DelFileByPathHash("/a.jpg"); err != nil || hash != "abc" {
		t.Fatalf("got %q, %v", hash, err)
	}

	for _, viaHash := range []bool{true, false} {
		mock.ExpectBegin()
		mock.ExpectQuery("select `hash` from `files` where `path` = \\?").
			WithArgs("/none").WillReturnRows(sqlmock.NewRows([]string{"hash"}))
		mock.ExpectRollback()
		if viaHash {
			if _, err := d.DelFileByPathHash("/none"); err != sql.ErrNoRows {
				t.Fatalf("want sql.ErrNoRows, got %v", err)
			}
		} else if err := d.DelFileByPath("/none"); err != nil {
			t.Fatalf("DelFileByPath of a missing path: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}
