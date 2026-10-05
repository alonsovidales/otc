// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A friend's unlike removes that friend's like - not stored as one more,
// whatever domain the payload names - and notifies nobody.
func TestFriendUnlikeRemovesItsLike(t *testing.T) {
	fr, mock := friendFrom(t, "x.off-the.cloud")
	mock.ExpectExec("delete from `social_publication_likes` where `pub_uuid` = \\? and `friend_domain` = \\?").
		WithArgs("p1", "x.off-the.cloud").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `social_publications` set `likes` = greatest").
		WithArgs(int64(1), "p1").WillReturnResult(sqlmock.NewResult(0, 1))
	fr.applyLike(&pb.Event{Type: LikeEvent, Content: `{"uuid":"l2","action":"delete","pub_uuid":"p1","friend_domain":"y.off-the.cloud"}`}, false)

	mock.ExpectExec("delete from `social_publication_comment_likes` where `comment_uuid` = \\? and `friend_domain` = \\?").
		WithArgs("c1", "x.off-the.cloud").WillReturnResult(sqlmock.NewResult(0, 0))
	fr.applyCommentLike(&pb.Event{Type: LikeCommentEvent, Content: `{"uuid":"l3","action":"delete","comment_uuid":"c1"}`}, false)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A like from a release that sends no action is still a like; one already
// stored is not notified again.
func TestFriendLikeWithoutActionIsALike(t *testing.T) {
	fr, mock := friendFrom(t, "x.off-the.cloud")
	mock.ExpectBegin()
	mock.ExpectExec("insert into `social_publication_likes`").
		WithArgs("l1", "p1", sqlmock.AnyArg(), "x.off-the.cloud", "p1", "x.off-the.cloud").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `social_publications` set `likes` = `likes` \\+ 1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// A new like is checked for a notification: not the owner's post here.
	mock.ExpectQuery("select `own_publication` from `social_publications`").
		WillReturnRows(sqlmock.NewRows([]string{"own_publication"}).AddRow(false))
	fr.applyLike(&pb.Event{Type: LikeEvent, Content: `{"uuid":"l1","pub_uuid":"p1"}`}, false)

	// The same domain again: nothing stored, so no ownership check either.
	mock.ExpectBegin()
	mock.ExpectExec("insert into `social_publication_likes`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	fr.applyLike(&pb.Event{Type: LikeEvent, Content: `{"uuid":"l4","pub_uuid":"p1"}`}, false)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
