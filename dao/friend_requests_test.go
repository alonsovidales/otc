// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"bytes"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// A friend request is one unread alert per domain: the first adds it, one
// sent again while it is unread moves it up.
func TestAddFriendRequestNotification(t *testing.T) {
	d, mock := ratingsMock(t)
	const pick = "select `uuid` from `notifications` where `type` = \\? and `actor_domain` = \\? and `acknowledged` = 0 limit 1"
	mock.ExpectQuery(pick).WithArgs("FriendRequest", "ann.off-the.cloud").WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
	mock.ExpectExec("insert into `notifications`").WillReturnResult(sqlmock.NewResult(1, 1))
	if added, err := d.AddFriendRequestNotification("Ann", "ann.off-the.cloud"); err != nil || !added {
		t.Fatalf("first: added %v, %v", added, err)
	}
	mock.ExpectQuery(pick).WithArgs("FriendRequest", "ann.off-the.cloud").WillReturnRows(sqlmock.NewRows([]string{"uuid"}).AddRow("n1"))
	mock.ExpectExec("update `notifications` set `dt` = now\\(\\), `actor_name` = \\? where `uuid` = \\?").WithArgs("Ann B", "n1").WillReturnResult(sqlmock.NewResult(0, 1))
	if added, err := d.AddFriendRequestNotification("Ann B", "ann.off-the.cloud"); err != nil || added {
		t.Fatalf("again: added %v, %v", added, err)
	}
}

// A friend's picture stored over the cap - before it, or by an older
// binary - is read back as none, and dropped at start.
func TestOversizedFriendPictures(t *testing.T) {
	d, mock := ratingsMock(t)
	small, large := []byte("small"), bytes.Repeat([]byte{1}, MaxProfilePictureBytes+1)
	for _, c := range []struct {
		stored []byte
		want   int
	}{{small, len(small)}, {large, 0}} {
		mock.ExpectQuery("select `status`, `name`, `image`, `text`, `sent` from `social_friendship` where `domain` = \\?").
			WillReturnRows(sqlmock.NewRows([]string{"status", "name", "image", "text", "sent"}).AddRow("accepted", "Bo", c.stored, "", true))
		if _, _, img, err := d.GetFriendProfile("bo.off-the.cloud"); err != nil || len(img) != c.want {
			t.Errorf("%d bytes stored: read %d, %v", len(c.stored), len(img), err)
		}
	}
	mock.ExpectExec("update `social_friendship` set `image` = null where length\\(`image`\\) > \\?").WithArgs(MaxProfilePictureBytes).WillReturnResult(sqlmock.NewResult(0, 2))
	if n, err := d.DropOversizedFriendPictures(); err != nil || n != 2 {
		t.Fatalf("dropped %d, %v", n, err)
	}
}
