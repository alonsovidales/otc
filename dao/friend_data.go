// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// Issue #174: what "everything from a friend" is on this device, and the
// ownership checks that keep one friend from removing another's data.

// NewEventFor logs an event that only target's device is served (a
// "forget me" to an ex-friend); every other friend's GetEvents skips it.
func (dao *Dao) NewEventFor(eventType string, data []byte, target string) error {
	_, err := dao.db.Exec("insert into `events` (`uuid`, `dt`, `type`, `content`, `target`) values (?, now(), ?, ?, ?)",
		uuid.New(), eventType, data, target)
	return err
}

// PublicationOwner is who a publication cached here belongs to: own is
// the device owner's; otherwise friendDomain is the friend whose it is.
// found is false for a publication this device doesn't have.
func (dao *Dao) PublicationOwner(pubUuid string) (friendDomain string, own, found bool, err error) {
	err = dao.db.QueryRow("select `friend_domain`, `own_publication` from `social_publications` where `uuid` = ?", pubUuid).Scan(&friendDomain, &own)
	if err == sql.ErrNoRows {
		return "", false, false, nil
	}
	return friendDomain, own, err == nil, err
}

// CommentInfo is where a comment is and whose it is: authorDomain is the
// device it was synced from ("" for comments stored before issue #174,
// whose author isn't known), ownComment whether the device owner wrote it.
func (dao *Dao) CommentInfo(commentUuid string) (pubUuid, authorDomain string, ownComment, found bool, err error) {
	err = dao.db.QueryRow("select `pub_uuid`, `author_domain`, `own_comment` from `social_publications_comments` where `uuid` = ?", commentUuid).
		Scan(&pubUuid, &authorDomain, &ownComment)
	if err == sql.ErrNoRows {
		return "", "", false, false, nil
	}
	return pubUuid, authorDomain, ownComment, err == nil, err
}

// FriendPublicationUuids lists the posts of domain cached here - never the
// device owner's own, whatever friend_domain says.
func (dao *Dao) FriendPublicationUuids(domain string) (uuids []string, err error) {
	rows, err := dao.db.Query("select `uuid` from `social_publications` where `friend_domain` = ? and `own_publication` = 0", domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		uuids = append(uuids, u)
	}
	return uuids, rows.Err()
}

// PurgeFriendActivity removes domain's comments (and the likes on them),
// its likes on anyone's posts and comments - the counters kept right - and
// the alerts about it. Its posts go separately (FriendPublicationUuids +
// the social package's removePublication, which also removes media).
func (dao *Dao) PurgeFriendActivity(domain string) error {
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		// Likes by domain, counters first (while the rows still exist).
		"update `social_publications` p set `likes` = greatest(`likes` - (select count(*) from `social_publication_likes` l where l.`pub_uuid` = p.`uuid` and l.`friend_domain` = ?), 0)",
		"delete from `social_publication_likes` where `friend_domain` = ?",
		"update `social_publications_comments` c set `likes` = greatest(`likes` - (select count(*) from `social_publication_comment_likes` l where l.`comment_uuid` = c.`uuid` and l.`friend_domain` = ?), 0)",
		"delete from `social_publication_comment_likes` where `friend_domain` = ?",
		// domain's comments, with whatever likes they had.
		"delete from `social_publication_comment_likes` where `comment_uuid` in (select `uuid` from `social_publications_comments` where `author_domain` = ?)",
		"delete from `social_publications_comments` where `author_domain` = ?",
		"delete from `notifications` where `actor_domain` = ?",
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q, domain); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetForgetRequested marks the friendship with domain as leaving: this
// device asked domain's to delete what it shared, and waits for it.
func (dao *Dao) SetForgetRequested(domain string, at time.Time) error {
	_, err := dao.db.Exec("update `social_friendship` set `forget_requested` = ? where `domain` = ?", at, domain)
	return err
}

// FriendshipAccess is what a friend connected right now may still do
// (issue #174: checked on every request, not only when it signed in -
// an unfriended device kept its open connection): accepted is false once
// the friendship is gone or no longer accepted; leaving is true while this
// device waits for that friend to delete what it shared.
func (dao *Dao) FriendshipAccess(domain string) (accepted, leaving bool, err error) {
	var status string
	err = dao.db.QueryRow("select `status`, `forget_requested` is not null from `social_friendship` where `domain` = ?", domain).Scan(&status, &leaving)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	return status == "accepted", leaving, err
}
