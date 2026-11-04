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
// rated from 1 to 5 stars with a comment each, by the owner and by the
// visitors of the collection's rated shared links. These are its queries;
// files_manager/rating.go owns the rules and the sealing. The stars are
// plain numbers the searches filter and sort by (as file_tags' scores); a
// comment is stored sealed, never in the clear. A database release 163's
// script hasn't reached answers ErrSchemaBehind (or nothing, for the reads
// that only add to an answer).
//
// Each one who rates keeps their own rating of a photo (release 169):
// photo_rating_sources has one row per (collection, photo, source), the
// source "" for the owner's own and a link's uuid for its visitors'. A
// link's visitors see and change only their link's row; until then every
// link and the owner shared one row, so one client saw another's comments
// and the owner's own notes, and could overwrite or clear them.
// photo_ratings stays what the owner sees and the searches use, as before:
// the latest change of any source, its link saying whose (from_link).

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
	// Comment is sealed to the user's public key. SharedComment, a link's
	// rating only, is the same comment sealed for that link's visitors
	// (photo_rating_sources.link_comment). Both nil without a comment.
	Comment, SharedComment []byte
	// Link is whose rating it is: the shared link it came through, "" for
	// the owner's own.
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

// SetPhotoRating stores r as its source's rating of the photo in the
// collection (r.Link: a link's visitors', "" the owner's own) and makes
// what the owner sees (photo_ratings) this change, the latest - in one
// transaction. A link's rating with neither stars nor a comment is taken
// back, and what the owner sees then falls back to the latest rating left
// when it was that link's; the owner's own is kept as a clear (a row with
// neither), which hides every older one from the owner, as clearing
// always did. Rows are written only while the photo is a member (the
// inserts select its membership row, which a removal deletes with its
// ratings in one transaction): stored is false for a photo taken out
// meanwhile. Times are kept to the microsecond in the sources, so of two
// changes in one second the later is the latest.
func (dao *Dao) SetPhotoRating(groupID string, r PhotoRatingRow) (stored bool, err error) {
	tx, err := dao.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	clear := r.Stars == 0 && len(r.Comment) == 0
	if clear && r.Link != "" {
		if _, err := tx.Exec("delete from `photo_rating_sources` where `group_id` = ? and `hash` = ? and `source` = ?", groupID, r.Hash, r.Link); err != nil {
			return false, ratingSchemaBehind(err)
		}
		var shown sql.NullString
		err := tx.QueryRow("select `link` from `photo_ratings` where `group_id` = ? and `hash` = ? for update", groupID, r.Hash).Scan(&shown)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, ratingSchemaBehind(err)
		}
		if err == nil && shown.String == r.Link {
			if err := showLatestRating(tx, groupID, r.Hash); err != nil {
				return false, err
			}
		}
		return true, tx.Commit()
	}

	var comment, linkComment any
	if len(r.Comment) > 0 {
		comment = r.Comment
		if r.Link != "" && len(r.SharedComment) > 0 {
			linkComment = r.SharedComment
		}
	}
	res, err := tx.Exec("insert into `photo_rating_sources` (`group_id`, `hash`, `source`, `stars`, `comment`, `link_comment`, `updated`)"+
		" select `group_id`, `hash`, ?, ?, ?, ?, ? from `image_group_files` where `group_id` = ? and `hash` = ?"+
		" on duplicate key update `stars` = values(`stars`), `comment` = values(`comment`), `link_comment` = values(`link_comment`), `updated` = values(`updated`)",
		r.Link, r.Stars, comment, linkComment, r.Updated.UTC(), groupID, r.Hash)
	if err != nil {
		return false, ratingSchemaBehind(err)
	}
	// 1 inserted, 2 updated; 0 for a row left as it was, which only a
	// member has.
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else if n == 0 {
		var one int
		err := tx.QueryRow("select 1 from `image_group_files` where `group_id` = ? and `hash` = ?", groupID, r.Hash).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	if clear {
		if _, err := tx.Exec("delete from `photo_ratings` where `group_id` = ? and `hash` = ?", groupID, r.Hash); err != nil {
			return false, ratingSchemaBehind(err)
		}
	} else if err := showRating(tx, groupID, r); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// showRating makes r what the owner sees of the photo (photo_ratings): its
// stars, its comment sealed to the owner's key, whose it is, its time to
// the second. Never a copy for links: each link's own is in its source.
func showRating(tx *sql.Tx, groupID string, r PhotoRatingRow) error {
	var link, comment any
	if r.Link != "" {
		link = r.Link
	}
	if len(r.Comment) > 0 {
		comment = r.Comment
	}
	_, err := tx.Exec("insert into `photo_ratings` (`group_id`, `hash`, `stars`, `comment`, `shared_comment`, `link`, `updated`)"+
		" select `group_id`, `hash`, ?, ?, null, ?, ? from `image_group_files` where `group_id` = ? and `hash` = ?"+
		" on duplicate key update `stars` = values(`stars`), `comment` = values(`comment`), `shared_comment` = null,"+
		" `link` = values(`link`), `updated` = values(`updated`)",
		r.Stars, comment, link, r.Updated.UTC().Truncate(time.Second), groupID, r.Hash)
	return ratingSchemaBehind(err)
}

// showLatestRating makes what the owner sees of the photo the latest
// rating left of any source - none when there is none, or when it is the
// owner's own clear.
func showLatestRating(tx *sql.Tx, groupID, hash string) error {
	r := PhotoRatingRow{Hash: hash}
	err := tx.QueryRow("select `source`, `stars`, `comment`, `updated` from `photo_rating_sources` where `group_id` = ? and `hash` = ? order by `updated` desc limit 1",
		groupID, hash).Scan(&r.Link, &r.Stars, &r.Comment, &r.Updated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ratingSchemaBehind(err)
	}
	if errors.Is(err, sql.ErrNoRows) || (r.Stars == 0 && len(r.Comment) == 0) {
		_, err := tx.Exec("delete from `photo_ratings` where `group_id` = ? and `hash` = ?", groupID, hash)
		return ratingSchemaBehind(err)
	}
	return showRating(tx, groupID, r)
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

// LinkRatings is the ratings the visitors of the shared link gave the
// photos (content hashes) of its collection, by hash: theirs only - never
// the owner's own nor another link's. SharedComment is the comment sealed
// for them. A database before release 169 has none.
func (dao *Dao) LinkRatings(groupID, link string, hashes []string) (map[string]PhotoRatingRow, error) {
	out := map[string]PhotoRatingRow{}
	if link == "" {
		return out, nil
	}
	for _, chunk := range hashChunks(hashes) {
		args := []any{groupID, link}
		for _, h := range chunk {
			args = append(args, h)
		}
		rows, err := dao.db.Query("select `hash`, `stars`, `link_comment`, `updated` from `photo_rating_sources`"+
			" where `group_id` = ? and `source` = ? and `hash` in (?"+strings.Repeat(", ?", len(chunk)-1)+")", args...)
		if isNoSuchTable(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r := PhotoRatingRow{Link: link}
			if err := rows.Scan(&r.Hash, &r.Stars, &r.SharedComment, &r.Updated); err != nil {
				rows.Close()
				return nil, err
			}
			out[r.Hash] = r
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ReconcileRatingSources gives every rating of what the owner sees
// (photo_ratings) its source's row when it has none, or one older than it
// - every rating stored before release 169 (a link's comment for its
// visitors from its shared_comment), and any written meanwhile by an older
// binary put back by an update that didn't start - and drops the owner's
// own comments' copies under a collection's link key: no link's visitors
// read them any more. Idempotent; run at each start. A database before
// release 169 is left as it is (ErrSchemaBehind).
func (dao *Dao) ReconcileRatingSources() error {
	// The target's columns named in full: photo_ratings has most of them.
	const s = "`photo_rating_sources`."
	if _, err := dao.db.Exec("insert into `photo_rating_sources` (`group_id`, `hash`, `source`, `stars`, `comment`, `link_comment`, `updated`)" +
		" select `pr`.`group_id`, `pr`.`hash`, coalesce(`pr`.`link`, ''), `pr`.`stars`, `pr`.`comment`, case when `pr`.`link` is not null then `pr`.`shared_comment` end, `pr`.`updated`" +
		" from `photo_ratings` as `pr`" +
		" on duplicate key update " + s + "`stars` = if(values(`updated`) > " + s + "`updated`, values(`stars`), " + s + "`stars`)," +
		" " + s + "`comment` = if(values(`updated`) > " + s + "`updated`, values(`comment`), " + s + "`comment`)," +
		" " + s + "`link_comment` = if(values(`updated`) > " + s + "`updated`, values(`link_comment`), " + s + "`link_comment`)," +
		" " + s + "`updated` = greatest(" + s + "`updated`, values(`updated`))"); err != nil {
		return ratingSchemaBehind(err)
	}
	_, err := dao.db.Exec("update `photo_ratings` set `shared_comment` = null where `link` is null and `shared_comment` is not null")
	return ratingSchemaBehind(err)
}

// ratingSchemaBehind wraps a missing table's or column's error in
// ErrSchemaBehind (release 169's photo_rating_sources, 163's columns).
func ratingSchemaBehind(err error) error {
	if isNoSuchTable(err) || isUnknownColumn(err) {
		return fmt.Errorf("%w: %v", ErrSchemaBehind, err)
	}
	return err
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
