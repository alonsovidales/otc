// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"sync"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// MaxProfilePictureBytes is the largest profile picture kept, the owner's
// or a friend's (files_manager.FitProfilePicture makes a larger one small
// or drops it). A friend's picture goes out again with every Alerts row
// and feed post of theirs: a friend request carrying 7.5 MB, sent again
// and again, made opening Alerts read and send hundreds of MB. The apps'
// crop is at most 45 KB, the setup wizard's 96 KB.
const MaxProfilePictureBytes = 128 << 10

// storedPicture is a friend's picture as read back: none when it is over
// MaxProfilePictureBytes (stored before the cap, or by an older binary).
func storedPicture(b []byte) []byte {
	if len(b) > MaxProfilePictureBytes {
		return nil
	}
	return b
}

// DropOversizedFriendPictures forgets the friends' pictures stored over
// MaxProfilePictureBytes before the cap: a friend's device is asked for its
// picture again at the next sync, and an accepted friend's large one is
// then kept made small. Answers how many were dropped.
func (dao *Dao) DropOversizedFriendPictures() (int64, error) {
	res, err := dao.db.Exec("update `social_friendship` set `image` = null where length(`image`) > ?", MaxProfilePictureBytes)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// friendRequestAlerts serialises AddFriendRequestNotification: of two
// requests from one domain at once, one adds the alert.
var friendRequestAlerts sync.Mutex

// AddFriendRequestNotification records a friend request from domain in
// Alerts: one unread alert per domain. A request sent again while the
// owner hasn't seen the first moves that alert up (its time, the name the
// request gave) instead of adding another; added says whether a new alert
// was made - what is worth a push. A stranger re-sending a request in a
// loop used to add an alert and push to every phone each time.
func (dao *Dao) AddFriendRequestNotification(actorName, actorDomain string) (added bool, err error) {
	friendRequestAlerts.Lock()
	defer friendRequestAlerts.Unlock()
	kind := dao.notificationTypeToStr(pb.NotificationType_NotificationFriendRequest)
	var id string
	err = dao.db.QueryRow("select `uuid` from `notifications` where `type` = ? and `actor_domain` = ? and `acknowledged` = 0 limit 1", kind, actorDomain).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return true, dao.NewNotification(pb.NotificationType_NotificationFriendRequest, actorName, actorDomain, "", "")
	case err != nil:
		return false, err
	}
	_, err = dao.db.Exec("update `notifications` set `dt` = now(), `actor_name` = ? where `uuid` = ?", actorName, id)
	return false, err
}
