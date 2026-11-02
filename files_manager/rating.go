// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Collection Rating (release 163; owner, 2026-11-02): a collection whose
// Rating is on has its photos rated from 1 to 5 stars with a comment each
// - one rating per photo and collection, the same for the owner and for
// anyone with one of the collection's shared links made with "Let people
// rate the photos" (a photographer's customer picking photos and asking
// for changes). The collection can then be filtered and sorted by it.
//
// The stars are plain numbers in the database (photo_ratings.stars), as
// tags' scores are: the searches filter and sort by them. A comment never
// is. It is sealed to the user's public key (vault.rating_public, X25519:
// an ephemeral key, HKDF-SHA256, AES-256-GCM - sealToPublic), so a comment
// that comes through a link while the owner is signed out can be stored,
// and it is opened with the private key, which is sealed with the user's
// own key (session.Encrypt) and so only usable once the owner has signed
// in. A password change only re-wraps the vault's secret, never the data
// key that sealed the private key, so the pair keeps working across it.
//
// A link's visitor must see each photo's current comment - their own, the
// photographer's reply, another visitor's - while the owner may be signed
// out, which the owner's key pair can't give. So each rated collection has
// one more key, its link key (image_groups.rating_key: 32 random bytes
// sealed with the owner's key), and every comment is also stored sealed
// with it (photo_ratings.shared_comment, AES-256-GCM). Each of the
// collection's rated links carries the link key inside its own manifest,
// which is sealed under the link's secret: a visitor's request (which
// always presents the secret) opens the manifest, and with it the link key
// - so the device can show and store comments for that visitor, and only
// while serving the visitor's request. The device keeps no link secret,
// and nothing on its disk opens a comment without either the owner's key
// or a link. The link key is made with the collection's first rated link,
// when the owner is signed in: the comments made before get their copy
// then (collectionLinkKey). One key per collection rather than per link,
// so a comment left through one link, while the owner is signed out,
// reaches the visitors of the collection's other links too. Galleries made
// before release 163 have no link key and no photo hashes in their
// manifest: they can't take ratings.

const (
	// cMaxRatingComment is the longest comment, in characters.
	cMaxRatingComment = 2000
	// cRatingAlertEvery: ratings through one link alert (and push) the
	// owner at most this often.
	cRatingAlertEvery = time.Hour
	// cSealV1 is the first byte of a sealed comment: the format.
	cSealV1 = 1
)

var (
	// ErrRatingOff: the collection's Rating is off (error_code
	// "rating_off").
	ErrRatingOff = devtext.New(i18n.DevFilesRatingOff())
	// ErrRatingLinkOff: a link that doesn't take ratings, or no longer
	// does (error_code "rating_off").
	ErrRatingLinkOff = devtext.New(i18n.DevFilesRatingLinkOff())
	// ErrNotInCollection: the photo isn't in the collection (any more)
	// (error_code "not_in_collection").
	ErrNotInCollection = devtext.New(i18n.DevFilesRatingNotInCollection())
	// ErrNoSuchCollection: the collection is gone.
	ErrNoSuchCollection = devtext.New(i18n.DevFilesRatingNoCollection())
	// ErrRatingStars: stars outside 0-5.
	ErrRatingStars = devtext.New(i18n.DevFilesRatingBadStars())
	// ErrRatingComment: a comment over cMaxRatingComment characters.
	ErrRatingComment = devtext.New(i18n.DevFilesRatingCommentTooLong(cMaxRatingComment))
)

// RatingNotice is what an alert about ratings left through a link names:
// the collection.
type RatingNotice struct {
	GroupName  string
	Favourites bool
}

// normalizeRating checks a rating as an app sends it: 0-5 stars, a comment
// trimmed, at most cMaxRatingComment characters.
func normalizeRating(stars int32, comment string) (int, string, error) {
	if stars < 0 || stars > 5 {
		return 0, "", ErrRatingStars
	}
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > cMaxRatingComment {
		return 0, "", ErrRatingComment
	}
	return int(stars), comment, nil
}

// ratingAAD binds a sealed comment to its photo and collection: a sealed
// comment copied onto another row doesn't open.
func ratingAAD(groupID, hash string) []byte {
	return []byte("otc-rating-comment\x00" + groupID + "\x00" + hash)
}

// sealToPublic seals plaintext to an X25519 public key: an ephemeral key
// pair, the shared secret through HKDF-SHA256 (salted with both public
// keys) into an AES-256-GCM key, used once. Laid out as the format byte,
// the ephemeral public key, the nonce and the ciphertext.
func sealToPublic(pub *ecdh.PublicKey, plaintext, aad []byte) ([]byte, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, err
	}
	aead, err := sealedCommentAEAD(shared, eph.PublicKey().Bytes(), pub.Bytes())
	if err != nil {
		return nil, err
	}
	out := append([]byte{cSealV1}, eph.PublicKey().Bytes()...)
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, aad), nil
}

// openWithPrivate opens what sealToPublic sealed to priv's public key.
func openWithPrivate(priv *ecdh.PrivateKey, sealed, aad []byte) ([]byte, error) {
	const pubLen = 32
	if len(sealed) < 1+pubLen || sealed[0] != cSealV1 {
		return nil, errors.New("not a sealed comment") // i18n-ignore: internal, never a reply
	}
	ephPub, err := ecdh.X25519().NewPublicKey(sealed[1 : 1+pubLen])
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(ephPub)
	if err != nil {
		return nil, err
	}
	aead, err := sealedCommentAEAD(shared, ephPub.Bytes(), priv.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	rest := sealed[1+pubLen:]
	if len(rest) < aead.NonceSize() {
		return nil, errors.New("sealed comment too short") // i18n-ignore: internal, never a reply
	}
	return aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], aad)
}

func sealedCommentAEAD(shared, ephPub, recipient []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, shared, append(append([]byte{}, ephPub...), recipient...), "otc-rating-comment-v1", 32)
	if err != nil {
		return nil, err
	}
	return newGCM(key)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealWithKey seals plaintext with a collection's link key (AES-256-GCM):
// the format byte, the nonce and the ciphertext.
func sealWithKey(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{cSealV1}, nonce...)
	return aead.Seal(out, nonce, plaintext, aad), nil
}

// openWithKey opens what sealWithKey sealed.
func openWithKey(key, sealed, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < 1+aead.NonceSize() || sealed[0] != cSealV1 {
		return nil, errors.New("not a sealed comment") // i18n-ignore: internal, never a reply
	}
	n := aead.NonceSize()
	return aead.Open(nil, sealed[1:1+n], sealed[1+n:], aad)
}

// EnsureRatingKeys makes the user's rating key pair when the vault has
// none (at a process' first sign-in: websocket's startBackfillOnce; and
// before a comment or a rated gallery needs it), and repairs a
// public key that isn't the private key's: the private key is sealed with
// the user's own key, so it is the one to trust. A database before release
// 163 has nowhere to keep it (dao.ErrSchemaBehind).
func (mg *Manager) EnsureRatingKeys(ses *session.Session) error {
	if mg == nil || mg.dao == nil || ses == nil {
		return nil
	}
	pub, sealedPriv, err := mg.dao.RatingKeys()
	if err != nil {
		return err
	}
	if len(sealedPriv) > 0 {
		priv, err := openRatingPrivate(ses, sealedPriv)
		if err != nil {
			return err
		}
		if want := priv.PublicKey().Bytes(); string(want) != string(pub) {
			log.Error("the rating public key wasn't the private key's: put back")
			return mg.dao.SetRatingPublic(want)
		}
		return nil
	}
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	return mg.dao.SetRatingKeysIfAbsent(k.PublicKey().Bytes(), ses.Encrypt(k.Bytes()))
}

func openRatingPrivate(ses *session.Session, sealed []byte) (*ecdh.PrivateKey, error) {
	raw, err := ses.Decrypt(sealed)
	if err != nil {
		return nil, fmt.Errorf("opening the rating key: %w", err) // i18n-ignore: internal, the reply is the generic error
	}
	return ecdh.X25519().NewPrivateKey(raw)
}

// ratingPublicKey is the key comments are sealed to; with ses (the owner
// signed in) it is made first when missing.
func (mg *Manager) ratingPublicKey(ses *session.Session) (*ecdh.PublicKey, error) {
	if ses != nil {
		if err := mg.EnsureRatingKeys(ses); err != nil {
			return nil, err
		}
	}
	pub, _, err := mg.dao.RatingKeys()
	if err != nil {
		return nil, err
	}
	if len(pub) == 0 {
		return nil, errors.New("no rating key yet") // i18n-ignore: internal, the reply is the generic error
	}
	return ecdh.X25519().NewPublicKey(pub)
}

// ratingPrivateKey opens the user's private rating key with the owner's
// session.
func (mg *Manager) ratingPrivateKey(ses *session.Session) (*ecdh.PrivateKey, error) {
	_, sealed, err := mg.dao.RatingKeys()
	if err != nil {
		return nil, err
	}
	if len(sealed) == 0 {
		return nil, errors.New("no rating key yet") // i18n-ignore: internal, never a reply
	}
	return openRatingPrivate(ses, sealed)
}

// collectionLinkKey is a collection's link key, opened with the owner's
// session: nil while it has none, unless create, which makes it and gives
// the comments made so far their copy for the links' visitors.
func (mg *Manager) collectionLinkKey(ses *session.Session, groupID string, create bool) ([]byte, error) {
	sealed, err := mg.dao.ImageGroupRatingKey(groupID)
	if err != nil {
		return nil, err
	}
	if len(sealed) == 0 {
		if !create {
			return nil, nil
		}
		fresh := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, fresh); err != nil {
			return nil, err
		}
		if sealed, err = mg.dao.SetImageGroupRatingKeyIfAbsent(groupID, ses.Encrypt(fresh)); err != nil {
			return nil, err
		}
	}
	key, err := ses.Decrypt(sealed)
	if err != nil {
		return nil, fmt.Errorf("opening a collection's link key: %w", err) // i18n-ignore: internal, the reply is the generic error
	}
	if create {
		mg.copyCommentsForLinks(ses, groupID, key)
	}
	return key, nil
}

// copyCommentsForLinks gives every comment of a collection without one its
// copy under the collection's link key. A comment that can't be opened is
// logged and left: its link visitors don't see it.
func (mg *Manager) copyCommentsForLinks(ses *session.Session, groupID string, key []byte) {
	rows, err := mg.dao.CommentsWithoutSharedCopy(groupID)
	if err != nil || len(rows) == 0 {
		if err != nil {
			log.Error("listing a collection's comments for its link:", err)
		}
		return
	}
	priv, err := mg.ratingPrivateKey(ses)
	if err != nil {
		log.Error("no rating key to copy a collection's comments for its link:", err)
		return
	}
	for hash, r := range rows {
		aad := ratingAAD(groupID, hash)
		plain, err := openWithPrivate(priv, r.Comment, aad)
		if err != nil {
			log.Error("a rating comment didn't open, not copied for the link:", hash, err)
			continue
		}
		shared, err := sealWithKey(key, plain, aad)
		if err == nil {
			err = mg.dao.SetSharedComment(groupID, hash, r.Comment, shared)
		}
		if err != nil {
			log.Error("copying a rating comment for the link:", err)
		}
	}
}

// GroupRated says whether a collection's Rating is on (false for none).
func (mg *Manager) GroupRated(groupID string) bool {
	if groupID == "" {
		return false
	}
	_, on, err := mg.dao.ImageGroupRating(groupID)
	if err != nil {
		log.Error("reading a collection's Rating:", err)
		return false
	}
	return on
}

// SetImageGroupRating turns a collection's Rating on or off. Off keeps the
// ratings, hidden, until it is on again.
func (mg *Manager) SetImageGroupRating(id string, on bool) error {
	if id == dao.FavouritesGroupID {
		// Always there: made here if nothing has listed it yet.
		if err := mg.dao.EnsureFavourites(); err != nil {
			return err
		}
	}
	if err := mg.dao.SetImageGroupRating(id, on); err != nil {
		if errors.Is(err, dao.ErrNoSuchGroup) {
			return ErrNoSuchCollection
		}
		return err
	}
	return nil
}

// SetPhotoRating is the owner's rating of the photo at path in a
// collection: stars and comment both, replacing what it had (0 and ""
// clear it). Answers the rating as stored.
func (mg *Manager) SetPhotoRating(ses *session.Session, groupID, path string, stars int32, comment string) (*pb.PhotoRating, error) {
	n, text, err := normalizeRating(stars, comment)
	if err != nil {
		return nil, err
	}
	f, err := mg.dao.GetFileByPath(path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotInCollection
	}
	if err != nil {
		return nil, err
	}
	exists, on, member, err := mg.dao.RatingTarget(groupID, f.Hash)
	switch {
	case err != nil:
		return nil, err
	case !exists:
		return nil, ErrNoSuchCollection
	case !on:
		return nil, ErrRatingOff
	case !member:
		return nil, ErrNotInCollection
	}
	now := time.Now().UTC().Truncate(time.Second)
	row := dao.PhotoRatingRow{Hash: f.Hash, Stars: n, Updated: now}
	if text != "" {
		aad := ratingAAD(groupID, f.Hash)
		pub, err := mg.ratingPublicKey(ses)
		if err != nil {
			return nil, err
		}
		if row.Comment, err = sealToPublic(pub, []byte(text), aad); err != nil {
			return nil, err
		}
		key, err := mg.collectionLinkKey(ses, groupID, false)
		if err != nil {
			return nil, err
		}
		if key != nil {
			if row.SharedComment, err = sealWithKey(key, []byte(text), aad); err != nil {
				return nil, err
			}
		}
	}
	stored, err := mg.dao.SetPhotoRating(groupID, row)
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, ErrNotInCollection
	}
	if n == 0 && text == "" {
		return &pb.PhotoRating{}, nil
	}
	return &pb.PhotoRating{Stars: int32(n), Comment: text, Updated: timestamppb.New(now)}, nil
}

// setRatings gives a page of a rated collection's listing each photo's
// rating (File.rating), comments opened with the owner's key. A comment
// that doesn't open is left out of its rating (logged); a failed read
// leaves the page without ratings.
func (mg *Manager) setRatings(ses *session.Session, groupID string, page []*pb.File) error {
	if len(page) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(page))
	for _, f := range page {
		hashes = append(hashes, f.Hash)
	}
	rows, err := mg.dao.PhotoRatings(groupID, hashes)
	if err != nil || len(rows) == 0 {
		return err
	}
	var priv *ecdh.PrivateKey
	opened := map[string]string{}
	for hash, r := range rows {
		if len(r.Comment) == 0 {
			continue
		}
		if priv == nil {
			if priv, err = mg.ratingPrivateKey(ses); err != nil {
				log.Error("no rating key to read the comments:", err)
				break
			}
		}
		plain, err := openWithPrivate(priv, r.Comment, ratingAAD(groupID, hash))
		if err != nil {
			log.Error("a rating comment didn't open:", hash, err)
			continue
		}
		opened[hash] = string(plain)
	}
	for _, f := range page {
		r, ok := rows[f.Hash]
		if !ok {
			continue
		}
		f.Rating = &pb.PhotoRating{Stars: int32(r.Stars), Comment: opened[f.Hash], FromLink: r.Link != "", Updated: timestamppb.New(r.Updated)}
	}
	return nil
}

// galleryRatings is what a rated gallery's visitor sees of each item's
// rating now, by item index: nil when the link doesn't take ratings (any
// more) - made without them, its collection deleted or its Rating off.
func (mg *Manager) galleryRatings(id string, man *galleryManifest) (rated bool, stars map[int]int, comments map[int]string) {
	if len(man.RatingKey) == 0 {
		return false, nil, nil
	}
	groupID, rating, err := mg.dao.GetSharedLinkRating(id)
	if err != nil || !rating || groupID == "" {
		if err != nil {
			log.Error("reading a gallery's rating:", err)
		}
		return false, nil, nil
	}
	exists, on, err := mg.dao.ImageGroupRating(groupID)
	if err != nil || !exists || !on {
		if err != nil {
			log.Error("reading a gallery's collection:", err)
		}
		return false, nil, nil
	}
	var hashes []string
	for _, it := range man.Items {
		if it.Hash != "" {
			hashes = append(hashes, it.Hash)
		}
	}
	rows, err := mg.dao.PhotoRatings(groupID, hashes)
	if err != nil {
		log.Error("reading a gallery's ratings:", err)
		return true, nil, nil
	}
	stars, comments = map[int]int{}, map[int]string{}
	for i, it := range man.Items {
		r, ok := rows[it.Hash]
		if it.Hash == "" || !ok {
			continue
		}
		stars[i] = r.Stars
		if len(r.SharedComment) > 0 {
			if plain, err := openWithKey(man.RatingKey, r.SharedComment, ratingAAD(groupID, it.Hash)); err == nil {
				comments[i] = string(plain)
			} else {
				log.Error("a rating comment didn't open with the link's key:", err)
			}
		}
	}
	return true, stars, comments
}

// RateSharedGalleryItem is a link visitor's rating of one of the gallery's
// photos, stars and comment both, as SetPhotoRating's: the collection's
// own rating (from_link). ErrNoSuchGallery for a link that doesn't open
// (or an index it doesn't have), ErrRatingLinkOff for one that doesn't
// take ratings now, ErrNotInCollection for a photo that left the
// collection. A notice when the owner should be told (at most once an
// hour per link).
func (mg *Manager) RateSharedGalleryItem(id, secret string, index int, stars int32, comment string) (*RatingNotice, error) {
	man, _, err := mg.openGallery(id, secret)
	if err != nil {
		return nil, ErrNoSuchGallery
	}
	if index < 0 || index >= len(man.Items) {
		return nil, ErrNoSuchGallery
	}
	item := man.Items[index]
	if len(man.RatingKey) == 0 || item.Hash == "" {
		return nil, ErrRatingLinkOff
	}
	n, text, err := normalizeRating(stars, comment)
	if err != nil {
		return nil, err
	}
	groupID, rating, err := mg.dao.GetSharedLinkRating(id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchGallery
	}
	if err != nil {
		return nil, err
	}
	if !rating || groupID == "" {
		return nil, ErrRatingLinkOff
	}
	exists, on, member, err := mg.dao.RatingTarget(groupID, item.Hash)
	switch {
	case err != nil:
		return nil, err
	case !exists || !on:
		return nil, ErrRatingLinkOff
	case !member:
		return nil, ErrNotInCollection
	}
	now := time.Now().UTC().Truncate(time.Second)
	row := dao.PhotoRatingRow{Hash: item.Hash, Stars: n, Link: id, Updated: now}
	if text != "" {
		aad := ratingAAD(groupID, item.Hash)
		pub, err := mg.ratingPublicKey(nil)
		if err != nil {
			return nil, err
		}
		if row.Comment, err = sealToPublic(pub, []byte(text), aad); err != nil {
			return nil, err
		}
		if row.SharedComment, err = sealWithKey(man.RatingKey, []byte(text), aad); err != nil {
			return nil, err
		}
	}
	stored, err := mg.dao.SetPhotoRating(groupID, row)
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, ErrNotInCollection
	}
	claimed, err := mg.dao.ClaimRatingAlert(id, cRatingAlertEvery, now)
	if err != nil {
		log.Error("recording a rating alert:", err)
		return nil, nil
	}
	if !claimed {
		return nil, nil
	}
	name, err := mg.dao.ImageGroupName(groupID)
	if err != nil {
		log.Error("reading a rated collection's name:", err)
	}
	return &RatingNotice{GroupName: name, Favourites: groupID == dao.FavouritesGroupID}, nil
}
