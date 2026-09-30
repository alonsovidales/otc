// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #174: a friend's deletions apply to its own data only - its posts,
// its comments, and comments on its posts - never the owner's or another
// friend's.

func friendFrom(t *testing.T, domain string) (*friendship, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &friendship{
		dao:  dao.NewWithDB(db),
		data: &pb.Friendship{OriginProfile: &pb.Profile{Domain: domain}},
	}, mock
}

func pubRow(mock sqlmock.Sqlmock, friendDomain string, own bool) {
	mock.ExpectQuery("select `friend_domain`, `own_publication` from `social_publications`").
		WillReturnRows(sqlmock.NewRows([]string{"friend_domain", "own_publication"}).AddRow(friendDomain, own))
}

func commentRow(mock sqlmock.Sqlmock, pubUuid, author string) {
	mock.ExpectQuery("select `pub_uuid`, `author_domain`, `own_comment` from `social_publications_comments`").
		WillReturnRows(sqlmock.NewRows([]string{"pub_uuid", "author_domain", "own_comment"}).AddRow(pubUuid, author, false))
}

func TestFriendMayDeleteOnlyItsOwnPosts(t *testing.T) {
	cases := []struct {
		name  string
		owner string
		own   bool
		want  bool
	}{
		{"its own post", "x.off-the.cloud", false, true},
		{"another friend's post", "y.off-the.cloud", false, false},
		{"the owner's own post", "x.off-the.cloud", true, false},
	}
	for _, c := range cases {
		fr, mock := friendFrom(t, "x.off-the.cloud")
		pubRow(mock, c.owner, c.own)
		if got := fr.mayDeletePublication("p1"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// A post this device doesn't have.
	fr, mock := friendFrom(t, "x.off-the.cloud")
	mock.ExpectQuery("select `friend_domain`").WillReturnRows(sqlmock.NewRows([]string{"friend_domain", "own_publication"}))
	if fr.mayDeletePublication("nope") {
		t.Error("deleted a post that isn't here")
	}
}

func TestFriendMayDeleteOnlyItsCommentsOrCommentsOnItsPosts(t *testing.T) {
	// Written by the friend: allowed without looking at the post.
	fr, mock := friendFrom(t, "x.off-the.cloud")
	commentRow(mock, "p1", "x.off-the.cloud")
	if !fr.mayDeleteComment("c1") {
		t.Error("its own comment was refused")
	}

	// Someone else's comment on the friend's post: allowed.
	fr, mock = friendFrom(t, "x.off-the.cloud")
	commentRow(mock, "p1", "y.off-the.cloud")
	pubRow(mock, "x.off-the.cloud", false)
	if !fr.mayDeleteComment("c1") {
		t.Error("a comment on its own post was refused")
	}

	// Someone else's comment on someone else's post: refused.
	fr, mock = friendFrom(t, "x.off-the.cloud")
	commentRow(mock, "p1", "y.off-the.cloud")
	pubRow(mock, "z.off-the.cloud", false)
	if fr.mayDeleteComment("c1") {
		t.Error("deleted another friend's comment on another friend's post")
	}

	// The owner's comment on the owner's post: refused.
	fr, mock = friendFrom(t, "x.off-the.cloud")
	commentRow(mock, "p1", "me.off-the.cloud")
	pubRow(mock, "me.off-the.cloud", true)
	if fr.mayDeleteComment("c1") {
		t.Error("deleted the owner's comment on the owner's post")
	}
}
