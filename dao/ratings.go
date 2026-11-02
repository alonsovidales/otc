// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Collection Rating (release 163; owner, 2026-11-02): a collection's photos
// rated from 1 to 5 stars with a comment each, the same for the owner and
// for anyone with the collection's rated shared link. These are its
// queries; files_manager/rating.go owns the rules and the sealing. The
// stars are plain numbers the searches filter and sort by (as file_tags'
// scores); a comment is stored sealed (comment, shared_comment), never in
// the clear. A database release 163's script hasn't reached answers
// ErrSchemaBehind (or nothing, for the reads that only add to an answer).

// ErrNoSuchGroup is a collection id with no collection.
var ErrNoSuchGroup = errors.New("no such collection")

// RatingQuery is SearchPhotos' rating filter and order, for a collection
// whose Rating is on.
type RatingQuery struct {
	MinStars    int  // 1-5: at least this many stars; 0: any
	Unrated     bool // only the photos without stars (MinStars is then ignored)
	WithComment bool // only the photos with a comment
	ByRating    bool // the most stars first, then the newest taken
}

// Active reports whether q filters or orders anything: a search with an
// inactive query is a plain date search.
func (q *RatingQuery) Active() bool {
	return q != nil && (q.MinStars > 0 || q.Unrated || q.WithComment || q.ByRating)
}

// conditions are q's filters on the joined `pr` row (none for a photo
// without one): numbers only, written into the text.
func (q *RatingQuery) conditions() []string {
	var out []string
	switch {
	case q.Unrated:
		out = append(out, "coalesce(`pr`.`stars`, 0) = 0")
	case q.MinStars > 0:
		out = append(out, fmt.Sprintf("coalesce(`pr`.`stars`, 0) >= %d", min(q.MinStars, 5)))
	}
	if q.WithComment {
		out = append(out, "`pr`.`comment` is not null")
	}
	return out
}

// ImageGroupRating says whether the collection exists and whether its
// Rating is on. A database before release 163 reads as off.
func (dao *Dao) ImageGroupRating(id string) (exists, on bool, err error) {
	err = dao.db.QueryRow("select `rating` from `image_groups` where `id` = ?", id).Scan(&on)
	if isUnknownColumn(err) {
		var one int
		err = dao.db.QueryRow("select 1 from `image_groups` where `id` = ?", id).Scan(&one)
		on = false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, on, nil
}

// ImageGroupName is a collection's name as stored ("Favourites" for
// Favourites, whose name the apps get in their language).
func (dao *Dao) ImageGroupName(id string) (string, error) {
	var name string
	err := dao.db.QueryRow("select `name` from `image_groups` where `id` = ?", id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoSuchGroup
	}
	return name, err
}

// SetImageGroupRating turns a collection's Rating on or off; off keeps the
// ratings. ErrNoSuchGroup when there is no such collection.
func (dao *Dao) SetImageGroupRating(id string, on bool) error {
	exists, _, err := dao.ImageGroupRating(id)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNoSuchGroup
	}
	_, err = dao.db.Exec("update `image_groups` set `rating` = ? where `id` = ?", on, id)
	return schemaBehind(err)
}

// ImageGroupRatingKey is the collection's link key for its rated links'
// visitors, sealed with the owner's key; nil while it has none.
func (dao *Dao) ImageGroupRatingKey(id string) ([]byte, error) {
	var key []byte
	err := dao.db.QueryRow("select `rating_key` from `image_groups` where `id` = ?", id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchGroup
	}
	return key, schemaBehind(err)
}

// SetImageGroupRatingKeyIfAbsent stores sealed as the collection's link
// key unless it has one, and answers the one it has now: two first rated
// links at once agree on one key.
func (dao *Dao) SetImageGroupRatingKeyIfAbsent(id string, sealed []byte) ([]byte, error) {
	if _, err := dao.db.Exec("update `image_groups` set `rating_key` = ? where `id` = ? and `rating_key` is null", sealed, id); err != nil {
		return nil, schemaBehind(err)
	}
	return dao.ImageGroupRatingKey(id)
}

// RatingKeys is the user's rating key pair (vault): the X25519 public key
// in the clear and the private key sealed with the user's key. Both nil
// while there is none.
func (dao *Dao) RatingKeys() (public, sealedPrivate []byte, err error) {
	err = dao.db.QueryRow("select `rating_public`, `rating_private` from `vault` limit 1").Scan(&public, &sealedPrivate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	return public, sealedPrivate, schemaBehind(err)
}

// SetRatingKeysIfAbsent stores the key pair unless the vault has one: of
// two sign-ins making one at once, the first stored wins (the caller reads
// it back with RatingKeys).
func (dao *Dao) SetRatingKeysIfAbsent(public, sealedPrivate []byte) error {
	_, err := dao.db.Exec("update `vault` set `rating_public` = ?, `rating_private` = ? where `rating_public` is null", public, sealedPrivate)
	return schemaBehind(err)
}

// SetRatingPublic puts back the public half of the key pair (the private
// key's own: EnsureRatingKeys found another).
func (dao *Dao) SetRatingPublic(public []byte) error {
	_, err := dao.db.Exec("update `vault` set `rating_public` = ?", public)
	return schemaBehind(err)
}

// PhotoRatingRow is one photo's rating in a collection as stored.
type PhotoRatingRow struct {
	Hash  string
	Stars int
	// Comment is sealed to the user's public key; SharedComment the same
	// comment sealed with the collection's link key, for its rated links'
	// visitors (nil until the collection has one). Both nil without a
	// comment.
	Comment, SharedComment []byte
	// Link is the shared link the last change came through, "" for the
	// owner's own.
	Link    string
	Updated time.Time
}

// RatingTarget says whether a photo (by content hash) may be rated in a
// collection: whether the collection exists, its Rating is on, and the
// photo is in it.
func (dao *Dao) RatingTarget(groupID, hash string) (exists, on, member bool, err error) {
	err = dao.db.QueryRow("select `g`.`rating`, exists (select 1 from `image_group_files` as `m` where `m`.`group_id` = `g`.`id` and `m`.`hash` = ?)"+
		" from `image_groups` as `g` where `g`.`id` = ?", hash, groupID).Scan(&on, &member)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, false, nil
	}
	if err != nil {
		return false, false, false, schemaBehind(err)
	}
	return true, on, member, nil
}

// SetPhotoRating stores r as the photo's rating in the collection,
// replacing what it had; one with neither stars nor a comment deletes it.
// The row is written only while the photo is a member (the insert selects
// its membership row, which a removal deletes with its ratings in one
// transaction), so a photo taken out meanwhile leaves no rating behind:
// stored is false then.
func (dao *Dao) SetPhotoRating(groupID string, r PhotoRatingRow) (stored bool, err error) {
	if r.Stars == 0 && len(r.Comment) == 0 {
		_, err := dao.db.Exec("delete from `photo_ratings` where `group_id` = ? and `hash` = ?", groupID, r.Hash)
		return true, schemaBehind(err)
	}
	var link any
	if r.Link != "" {
		link = r.Link
	}
	var comment, shared any
	if len(r.Comment) > 0 {
		comment = r.Comment
		if len(r.SharedComment) > 0 {
			shared = r.SharedComment
		}
	}
	res, err := dao.db.Exec("insert into `photo_ratings` (`group_id`, `hash`, `stars`, `comment`, `shared_comment`, `link`, `updated`)"+
		" select `group_id`, `hash`, ?, ?, ?, ?, ? from `image_group_files` where `group_id` = ? and `hash` = ?"+
		" on duplicate key update `stars` = values(`stars`), `comment` = values(`comment`), `shared_comment` = values(`shared_comment`),"+
		" `link` = values(`link`), `updated` = values(`updated`)",
		r.Stars, comment, shared, link, r.Updated.UTC(), groupID, r.Hash)
	if err != nil {
		return false, schemaBehind(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	// 1 inserted, 2 updated; 0 for a row left as it was, which only a
	// member has (an update to the same values within the second).
	if n > 0 {
		return true, nil
	}
	var one int
	err = dao.db.QueryRow("select 1 from `photo_ratings` where `group_id` = ? and `hash` = ?", groupID, r.Hash).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// PhotoRatings is the ratings of the photos (content hashes) in a
// collection, by hash; photos without one aren't in it. A database before
// release 163 has none.
func (dao *Dao) PhotoRatings(groupID string, hashes []string) (map[string]PhotoRatingRow, error) {
	out := map[string]PhotoRatingRow{}
	for _, chunk := range hashChunks(hashes) {
		args := []any{groupID}
		for _, h := range chunk {
			args = append(args, h)
		}
		rows, err := dao.db.Query("select `hash`, `stars`, `comment`, `shared_comment`, `link`, `updated` from `photo_ratings`"+
			" where `group_id` = ? and `hash` in (?"+strings.Repeat(", ?", len(chunk)-1)+")", args...)
		if isNoSuchTable(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		err = scanRatings(rows, out)
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CommentsWithoutSharedCopy is a collection's ratings whose comment has no
// copy for its links' visitors yet: what was commented before the
// collection's first rated link.
func (dao *Dao) CommentsWithoutSharedCopy(groupID string) (map[string]PhotoRatingRow, error) {
	rows, err := dao.db.Query("select `hash`, `stars`, `comment`, `shared_comment`, `link`, `updated` from `photo_ratings`"+
		" where `group_id` = ? and `comment` is not null and `shared_comment` is null", groupID)
	if isNoSuchTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PhotoRatingRow{}
	return out, scanRatings(rows, out)
}

// SetSharedComment gives a rating's comment its copy for the collection's
// links' visitors, unless the comment changed meanwhile (sealedComment is
// the comment the copy was made from).
func (dao *Dao) SetSharedComment(groupID, hash string, sealedComment, shared []byte) error {
	_, err := dao.db.Exec("update `photo_ratings` set `shared_comment` = ? where `group_id` = ? and `hash` = ? and `comment` = ?",
		shared, groupID, hash, sealedComment)
	return schemaBehind(err)
}

func scanRatings(rows *sql.Rows, out map[string]PhotoRatingRow) error {
	for rows.Next() {
		var r PhotoRatingRow
		var link sql.NullString
		if err := rows.Scan(&r.Hash, &r.Stars, &r.Comment, &r.SharedComment, &link, &r.Updated); err != nil {
			return err
		}
		r.Link = link.String
		out[r.Hash] = r
	}
	return rows.Err()
}

// GetSharedLinkRating is the collection a gallery's visitors may rate:
// groupID and rating as the gallery was made (release 163). "" and false
// for a gallery made without Rating, before it, or on a database before
// it; sql.ErrNoRows for no such link.
func (dao *Dao) GetSharedLinkRating(uuid string) (groupID string, rating bool, err error) {
	var group sql.NullString
	err = dao.db.QueryRow("select `group_id`, `rating` from `shared_links` where `uuid` = ?", uuid).Scan(&group, &rating)
	if isUnknownColumn(err) {
		return "", false, nil
	}
	return group.String, rating, err
}

// ClaimRatingAlert says whether the ratings coming through a link may
// alert the owner now - at most once per every - and, when they may,
// records it: of two at once only one is told yes.
func (dao *Dao) ClaimRatingAlert(uuid string, every time.Duration, now time.Time) (bool, error) {
	now = now.UTC()
	res, err := dao.db.Exec("update `shared_links` set `rating_alerted` = ? where `uuid` = ? and (`rating_alerted` is null or `rating_alerted` <= ?)",
		now, uuid, now.Add(-every))
	if err != nil {
		return false, schemaBehind(err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
