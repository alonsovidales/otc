// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// commentColumns are what queryComments reads of each comment.
var commentColumns = []string{"pub_uuid", "uuid", "dt", "comment", "publisher_name", "author_domain", "likes", "own_comment", "liked"}

// A comment is shown with the name this device knows its author by - never
// the one a friend's device signed it with ("Mum", or the owner's own
// name): the friendship's for the device it came from, the owner's for
// theirs, the domain when there is no name to know it by, and the name it
// came with only for a row from before author_domain.
func TestCommentAuthor(t *testing.T) {
	names := map[string]string{"bob.off-the.cloud": "Bob", "nameless.off-the.cloud": ""}
	for _, c := range []struct {
		name                         string
		own                          bool
		author, stored, owner, wants string
	}{
		{"a friend signing as Mum", false, "bob.off-the.cloud", "Mum", "Ana", "Bob"},
		{"a friend signing as the owner", false, "bob.off-the.cloud", "Ana", "Ana", "Bob"},
		{"a friend without a name", false, "nameless.off-the.cloud", "Mum", "Ana", "nameless.off-the.cloud"},
		{"a friend removed since", false, "gone.off-the.cloud", "Mum", "Ana", "gone.off-the.cloud"},
		{"the owner's own, renamed since", true, "ana.off-the.cloud", "Ana Old", "Ana", "Ana"},
		{"the owner's own, without a name now", true, "ana.off-the.cloud", "Ana Old", "", "Ana Old"},
		{"a row from before release 51", false, "", "Carol", "Ana", "Carol"},
	} {
		if got := commentAuthor(c.own, c.author, c.stored, c.owner, names); got != c.wants {
			t.Errorf("%s: %q, want %q", c.name, got, c.wants)
		}
	}
}

// The names are read once the comments are (no second pooled connection
// while they are open), one query for every friend of the page.
func TestCommentsTakeTheirAuthorsFromTheFriendships(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	mock.ExpectQuery("from `social_publications_comments` c where c\\.`pub_uuid` = \\? order by c\\.`dt` desc").
		WithArgs("ana.off-the.cloud", "p1").
		WillReturnRows(sqlmock.NewRows(commentColumns).
			AddRow("p1", "c1", now, "hi", "Mum", "bob.off-the.cloud", 0, false, 0).
			AddRow("p1", "c2", now, "hello", "Ana", "bob.off-the.cloud", 0, false, 0).
			AddRow("p1", "c3", now, "mine", "Ana Old", "ana.off-the.cloud", 0, true, 0).
			AddRow("p1", "c4", now, "old", "Carol", "", 0, false, 0))
	mock.ExpectQuery("select `domain`, `name` from `social_friendship` where `domain` in \\(\\?\\)").
		WithArgs("bob.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"domain", "name"}).AddRow("bob.off-the.cloud", "Bob"))

	comments, err := NewWithDB(db).GetSocialPublicationComments("p1", "ana.off-the.cloud", "Ana")
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range comments {
		got = append(got, c.Publisher)
	}
	if want := []string{"Bob", "Bob", "Ana", "Carol"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("shown as %q, want %q", got, want)
	}

	// No friend's comment on the page: no names asked.
	mock.ExpectQuery("from `social_publications_comments` c").
		WillReturnRows(sqlmock.NewRows(commentColumns).AddRow("p2", "c5", now, "mine", "Ana", "ana.off-the.cloud", 0, true, 0))
	if _, err := NewWithDB(db).GetSocialPublicationComments("p2", "ana.off-the.cloud", "Ana"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
