// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// A comment sealed to the public key opens only with its private key and
// only on its own row; one sealed with a collection's link key only with
// that key.
func TestRatingSeals(t *testing.T) {
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	aad := ratingAAD("g1", "h1")
	sealed, err := sealToPublic(priv.PublicKey(), []byte("Brighter, please"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("Brighter")) || sealed[0] != cSealV1 {
		t.Fatalf("sealed %x", sealed)
	}
	if got, err := openWithPrivate(priv, sealed, aad); err != nil || string(got) != "Brighter, please" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := openWithPrivate(other, sealed, aad); err == nil {
		t.Error("opened with another key")
	}
	if _, err := openWithPrivate(priv, sealed, ratingAAD("g1", "h2")); err == nil {
		t.Error("opened on another photo's row")
	}
	again, _ := sealToPublic(priv.PublicKey(), []byte("Brighter, please"), aad)
	if bytes.Equal(again, sealed) {
		t.Error("two seals of one comment are equal")
	}
	for _, bad := range [][]byte{nil, {cSealV1}, sealed[:40], append([]byte{2}, sealed[1:]...)} {
		if _, err := openWithPrivate(priv, bad, aad); err == nil {
			t.Errorf("opened %x", bad)
		}
	}

	key := make([]byte, 32)
	rand.Read(key)
	shared, err := sealWithKey(key, []byte("ok"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openWithKey(key, shared, aad); err != nil || string(got) != "ok" {
		t.Fatalf("open with the key: %q %v", got, err)
	}
	if _, err := openWithKey(make([]byte, 32), shared, aad); err == nil {
		t.Error("opened with another link key")
	}
	if _, err := openWithKey(key, shared, ratingAAD("g2", "h1")); err == nil {
		t.Error("opened in another collection")
	}
}

func TestNormalizeRating(t *testing.T) {
	if n, c, err := normalizeRating(5, "  Love it \n"); err != nil || n != 5 || c != "Love it" {
		t.Errorf("%d %q %v", n, c, err)
	}
	for _, s := range []int32{-1, 6} {
		if _, _, err := normalizeRating(s, ""); !errors.Is(err, ErrRatingStars) {
			t.Errorf("%d stars: %v", s, err)
		}
	}
	long := strings.Repeat("é", cMaxRatingComment)
	if _, c, err := normalizeRating(0, " "+long+" "); err != nil || c != long {
		t.Errorf("2000 characters (4000 bytes): %v", err)
	}
	if _, _, err := normalizeRating(0, long+"x"); !errors.Is(err, ErrRatingComment) {
		t.Errorf("2001 characters: %v", err)
	}
}

// ratingVault is a key pair as the vault keeps it, for the mock to answer.
type ratingVault struct {
	priv   *ecdh.PrivateKey
	sealed []byte
}

func newRatingVault(t *testing.T, ses *session.Session) ratingVault {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return ratingVault{priv, ses.Encrypt(priv.Bytes())}
}

func (v ratingVault) expect(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("select `rating_public`, `rating_private` from `vault` limit 1").
		WillReturnRows(sqlmock.NewRows([]string{"rating_public", "rating_private"}).AddRow(v.priv.PublicKey().Bytes(), v.sealed))
}

func ratingMock(t *testing.T) (*Manager, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return keptOut(&Manager{dao: dao.NewWithDB(db), sharedLinkTTL: time.Hour, searchTokens: newSearchTokenCache(1000)}), mock
}

// The key pair is made at a sign-in that finds none - its private half
// sealed with the user's key - kept when it is there, and a public half
// that isn't the private key's is put back.
func TestEnsureRatingKeys(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock := ratingMock(t)

	mock.ExpectQuery("select `rating_public`, `rating_private` from `vault`").
		WillReturnRows(sqlmock.NewRows([]string{"rating_public", "rating_private"}).AddRow(nil, nil))
	var pub, sealed []byte
	mock.ExpectExec("update `vault` set `rating_public` = \\?, `rating_private` = \\? where `rating_public` is null").
		WithArgs(descCapture{&pub}, descCapture{&sealed}).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.EnsureRatingKeys(ses); err != nil {
		t.Fatal(err)
	}
	priv, err := openRatingPrivate(ses, sealed)
	if err != nil || !bytes.Equal(priv.PublicKey().Bytes(), pub) || len(pub) != 32 {
		t.Fatalf("the pair stored: %v", err)
	}

	v := ratingVault{priv, sealed}
	v.expect(mock)
	if err := mg.EnsureRatingKeys(ses); err != nil {
		t.Fatalf("kept: %v", err)
	}

	mock.ExpectQuery("select `rating_public`, `rating_private` from `vault`").
		WillReturnRows(sqlmock.NewRows([]string{"rating_public", "rating_private"}).AddRow(bytes.Repeat([]byte{7}, 32), sealed))
	mock.ExpectExec("update `vault` set `rating_public` = \\?$").WithArgs(pub).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.EnsureRatingKeys(ses); err != nil {
		t.Fatalf("repaired: %v", err)
	}

	mock.ExpectQuery("select `rating_public`, `rating_private` from `vault`").WillReturnError(errors.New("down"))
	if err := mg.EnsureRatingKeys(ses); err == nil {
		t.Error("a failed read was no error")
	}
	if err := (*Manager)(nil).EnsureRatingKeys(ses); err != nil {
		t.Errorf("no manager: %v", err)
	}
}

func fileRowFor(path, hash string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
		AddRow(hash, "image/jpeg", time.Now(), time.Now(), path, 1)
}

const upsertRating = "insert into `photo_ratings` \\(`group_id`, `hash`, `stars`, `comment`, `shared_comment`, `link`, `updated`\\) select"

// The owner's rating: stars in the clear, the comment sealed to the key
// pair - and with the collection's link key once it has one - only in a
// collection whose Rating is on, for a photo in it.
func TestOwnerSetsPhotoRating(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock := ratingMock(t)
	v := newRatingVault(t, ses)
	target := func(exists, on, member bool) {
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").
			WithArgs("/p/a.jpg").WillReturnRows(fileRowFor("/p/a.jpg", "h1"))
		rows := sqlmock.NewRows([]string{"rating", "member"})
		if exists {
			rows.AddRow(on, member)
		}
		mock.ExpectQuery("select `g`.`rating`, exists").WithArgs("h1", "g1").WillReturnRows(rows)
	}

	target(true, true, true)
	v.expect(mock) // EnsureRatingKeys
	v.expect(mock) // the public key
	mock.ExpectQuery("select `rating_key` from `image_groups` where `id` = \\?").WithArgs("g1").
		WillReturnRows(sqlmock.NewRows([]string{"rating_key"}).AddRow(nil))
	var comment []byte
	mock.ExpectExec(upsertRating).WithArgs(5, descCapture{&comment}, nil, nil, sqlmock.AnyArg(), "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 1))
	r, err := mg.SetPhotoRating(ses, "g1", "/p/a.jpg", 5, " Love it ")
	if err != nil || r.Stars != 5 || r.Comment != "Love it" || r.FromLink || r.Updated == nil {
		t.Fatalf("%+v %v", r, err)
	}
	if got, err := openWithPrivate(v.priv, comment, ratingAAD("g1", "h1")); err != nil || string(got) != "Love it" {
		t.Fatalf("stored comment: %q %v", got, err)
	}

	// With a link key: the visitors' copy too.
	key := bytes.Repeat([]byte{3}, 32)
	target(true, true, true)
	v.expect(mock)
	v.expect(mock)
	mock.ExpectQuery("select `rating_key` from `image_groups`").WithArgs("g1").
		WillReturnRows(sqlmock.NewRows([]string{"rating_key"}).AddRow(ses.Encrypt(key)))
	var shared []byte
	mock.ExpectExec(upsertRating).WithArgs(2, sqlmock.AnyArg(), descCapture{&shared}, nil, sqlmock.AnyArg(), "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 2))
	if _, err := mg.SetPhotoRating(ses, "g1", "/p/a.jpg", 2, "Crop it"); err != nil {
		t.Fatal(err)
	}
	if got, err := openWithKey(key, shared, ratingAAD("g1", "h1")); err != nil || string(got) != "Crop it" {
		t.Fatalf("the visitors' copy: %q %v", got, err)
	}

	// Stars alone touch no key; 0 and "" clear it.
	target(true, true, true)
	mock.ExpectExec(upsertRating).WithArgs(3, nil, nil, nil, sqlmock.AnyArg(), "g1", "h1").WillReturnResult(sqlmock.NewResult(0, 2))
	if r, err := mg.SetPhotoRating(ses, "g1", "/p/a.jpg", 3, ""); err != nil || r.Stars != 3 || r.Comment != "" {
		t.Fatalf("stars alone: %+v %v", r, err)
	}
	target(true, true, true)
	mock.ExpectExec("delete from `photo_ratings` where `group_id` = \\? and `hash` = \\?").WithArgs("g1", "h1").WillReturnResult(sqlmock.NewResult(0, 1))
	if r, err := mg.SetPhotoRating(ses, "g1", "/p/a.jpg", 0, "  "); err != nil || r.Stars != 0 || r.Comment != "" || r.Updated != nil {
		t.Fatalf("cleared: %+v %v", r, err)
	}

	for _, c := range []struct {
		exists, on, member bool
		want               error
	}{{false, false, false, ErrNoSuchCollection}, {true, false, true, ErrRatingOff}, {true, true, false, ErrNotInCollection}} {
		target(c.exists, c.on, c.member)
		if _, err := mg.SetPhotoRating(ses, "g1", "/p/a.jpg", 4, ""); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	mock.ExpectQuery("from `files` where `path` = \\?").WithArgs("/p/gone.jpg").WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	if _, err := mg.SetPhotoRating(ses, "g1", "/p/gone.jpg", 4, ""); !errors.Is(err, ErrNotInCollection) {
		t.Errorf("a path that isn't there: %v", err)
	}
	if _, err := mg.SetPhotoRating(ses, "g1", "/p/a.jpg", 6, ""); !errors.Is(err, ErrRatingStars) {
		t.Errorf("6 stars: %v", err)
	}
	// Taken out of the collection between the check and the write.
	target(true, true, true)
	mock.ExpectExec(upsertRating).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("select 1 from `photo_ratings`").WillReturnRows(sqlmock.NewRows([]string{"1"}))
	if _, err := mg.SetPhotoRating(ses, "g1", "/p/a.jpg", 4, ""); !errors.Is(err, ErrNotInCollection) {
		t.Errorf("taken out meanwhile: %v", err)
	}

	// A page of the collection: each photo's rating, the comment opened.
	sealed, _ := sealToPublic(v.priv.PublicKey(), []byte("Love it"), ratingAAD("g1", "h1"))
	at := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	mock.ExpectQuery("from `photo_ratings` where `group_id` = \\? and `hash` in").WithArgs("g1", "h1", "h2").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "stars", "comment", "shared_comment", "link", "updated"}).AddRow("h1", 5, sealed, nil, "link-1", at))
	v.expect(mock)
	page := []*pb.File{{Hash: "h1"}, {Hash: "h2"}}
	if err := mg.setRatings(ses, "g1", page); err != nil {
		t.Fatal(err)
	}
	if r := page[0].Rating; r == nil || r.Stars != 5 || r.Comment != "Love it" || !r.FromLink || !r.Updated.AsTime().Equal(at) {
		t.Errorf("rated: %+v", r)
	}
	if page[1].Rating != nil {
		t.Errorf("unrated: %+v", page[1].Rating)
	}
}

// The first rated link of a collection makes its link key - of two made
// at once the one stored first is used - and gives the comments made
// before their copy under it.
func TestCollectionLinkKeyCopiesTheComments(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock := ratingMock(t)
	v := newRatingVault(t, ses)
	old, _ := sealToPublic(v.priv.PublicKey(), []byte("Older comment"), ratingAAD("g1", "h1"))
	first := bytes.Repeat([]byte{5}, 32)

	mock.ExpectQuery("select `rating_key` from `image_groups`").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"rating_key"}).AddRow(nil))
	var made []byte
	mock.ExpectExec("update `image_groups` set `rating_key` = \\? where `id` = \\? and `rating_key` is null").
		WithArgs(descCapture{&made}, "g1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("select `rating_key` from `image_groups`").WithArgs("g1").
		WillReturnRows(sqlmock.NewRows([]string{"rating_key"}).AddRow(ses.Encrypt(first)))
	mock.ExpectQuery("where `group_id` = \\? and `comment` is not null and `shared_comment` is null").WithArgs("g1").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "stars", "comment", "shared_comment", "link", "updated"}).AddRow("h1", 0, old, nil, nil, time.Now()))
	v.expect(mock)
	var shared []byte
	mock.ExpectExec("update `photo_ratings` set `shared_comment` = \\? where `group_id` = \\? and `hash` = \\? and `comment` = \\?").
		WithArgs(descCapture{&shared}, "g1", "h1", old).WillReturnResult(sqlmock.NewResult(0, 1))
	key, err := mg.collectionLinkKey(ses, "g1", true)
	if err != nil || !bytes.Equal(key, first) {
		t.Fatalf("%x %v, want the key stored first", key, err)
	}
	if opened, err := ses.Decrypt(made); err != nil || len(opened) != 32 || bytes.Equal(opened, first) {
		t.Fatalf("the key offered isn't a new one sealed with the owner's key: %v", err)
	}
	if got, err := openWithKey(key, shared, ratingAAD("g1", "h1")); err != nil || string(got) != "Older comment" {
		t.Fatalf("copied: %q %v", got, err)
	}
	// Without create, none is made.
	mock.ExpectQuery("select `rating_key` from `image_groups`").WithArgs("g2").WillReturnRows(sqlmock.NewRows([]string{"rating_key"}).AddRow(nil))
	if key, err := mg.collectionLinkKey(ses, "g2", false); err != nil || key != nil {
		t.Fatalf("made without create: %x %v", key, err)
	}
}

// A gallery made with Rating: its manifest holds the collection's link key
// and each photo's hash, its row the collection. Its visitors rate (their
// comment sealed to the owner's public key and under the link key), see
// every photo's current rating, and are refused once the link no longer
// takes ratings or the photo left; the owner is alerted at most once an
// hour per link.
func TestRatedSharedGallery(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	files := []*pb.File{
		libraryFile(t, ses, "pick-1.jpg", "image/jpeg", bytes.Repeat([]byte("ONE"), 1000)),
		libraryFile(t, ses, "pick-22.jpg", "image/jpeg", bytes.Repeat([]byte("TWO"), 1000)),
	}
	if files[0].Hash == files[1].Hash {
		t.Fatal("the two photos share a hash")
	}
	mg, mock := ratingMock(t)
	v := newRatingVault(t, ses)
	key := bytes.Repeat([]byte{9}, 32)

	mock.ExpectExec("insert into `shared_links` \\(`uuid`, `size`, `created`, `kind`, `description`, `files`, `expires`, `group_id`, `rating`\\)").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 2, sqlmock.AnyArg(), "g1").WillReturnResult(sqlmock.NewResult(1, 1))
	link, err := mg.buildSharedGallery(ses, files, "Pick your favourites", time.Hour, "cala.off-the.cloud", false, &galleryRating{GroupID: "g1", Key: key}, &galleryJob{state: &pb.SharedGalleryJob{}})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(strings.TrimPrefix(link, "https://cala.off-the.cloud/shared#"), ".", 2)
	id, secret := parts[0], parts[1]
	raw, _ := os.ReadFile(filepath.Join(storage, "shared", id, "manifest"))
	if bytes.Contains(raw, key) || bytes.Contains(raw, []byte(files[0].Hash)) {
		t.Fatal("the manifest holds the link key or a hash in the clear")
	}

	alive := func() {
		mock.ExpectQuery("select `created`, `expires` from `shared_links`").
			WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now(), time.Now().Add(time.Hour)))
	}
	linkRating := func(group string, rating bool) {
		mock.ExpectQuery("select `group_id`, `rating` from `shared_links` where `uuid` = \\?").WithArgs(id).
			WillReturnRows(sqlmock.NewRows([]string{"group_id", "rating"}).AddRow(group, rating))
	}
	target := func(hash string, on, member bool) {
		mock.ExpectQuery("select `g`.`rating`, exists").WithArgs(hash, "g1").
			WillReturnRows(sqlmock.NewRows([]string{"rating", "member"}).AddRow(on, member))
	}

	// A visitor's first rating: stored, and the owner told.
	alive()
	linkRating("g1", true)
	target(files[1].Hash, true, true)
	v.expect(mock)
	var comment, shared []byte
	mock.ExpectExec(upsertRating).WithArgs(4, descCapture{&comment}, descCapture{&shared}, id, sqlmock.AnyArg(), "g1", files[1].Hash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `shared_links` set `rating_alerted` = \\?").WithArgs(sqlmock.AnyArg(), id, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `name` from `image_groups` where `id` = \\?").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Wedding"))
	notice, err := mg.RateSharedGalleryItem(id, secret, 1, 4, " Brighter, please ")
	if err != nil || notice == nil || notice.GroupID != "g1" || notice.GroupName != "Wedding" || notice.Favourites {
		t.Fatalf("rate: %+v %v", notice, err)
	}
	aad := ratingAAD("g1", files[1].Hash)
	if got, err := openWithPrivate(v.priv, comment, aad); err != nil || string(got) != "Brighter, please" {
		t.Fatalf("the owner's copy: %q %v", got, err)
	}
	if got, err := openWithKey(key, shared, aad); err != nil || string(got) != "Brighter, please" {
		t.Fatalf("the visitors' copy: %q %v", got, err)
	}

	// Within the hour: stored, nobody told again. Stars alone need no key.
	alive()
	linkRating("g1", true)
	target(files[0].Hash, true, true)
	mock.ExpectExec(upsertRating).WithArgs(5, nil, nil, id, sqlmock.AnyArg(), "g1", files[0].Hash).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `shared_links` set `rating_alerted`").WillReturnResult(sqlmock.NewResult(0, 0))
	if notice, err := mg.RateSharedGalleryItem(id, secret, 0, 5, ""); err != nil || notice != nil {
		t.Fatalf("again: %+v %v", notice, err)
	}

	// The page shows every photo's rating now.
	alive()
	mock.ExpectExec("update `shared_links` set `opens`").WillReturnResult(sqlmock.NewResult(0, 1))
	linkRating("g1", true)
	mock.ExpectQuery("select `rating` from `image_groups` where `id` = \\?").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"rating"}).AddRow(true))
	mock.ExpectQuery("from `photo_ratings` where `group_id` = \\? and `hash` in").WithArgs("g1", files[0].Hash, files[1].Hash).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "stars", "comment", "shared_comment", "link", "updated"}).
			AddRow(files[0].Hash, 5, nil, nil, id, time.Now()).
			AddRow(files[1].Hash, 4, comment, shared, nil, time.Now()))
	g, err := mg.OpenSharedGallery(id, secret)
	if err != nil || !g.Rating || g.Items[0].Stars != 5 || g.Items[0].Comment != "" || g.Items[1].Stars != 4 || g.Items[1].Comment != "Brighter, please" {
		t.Fatalf("open: %+v %v", g, err)
	}

	// The collection's Rating off: the page takes none, and a rating is
	// refused.
	alive()
	mock.ExpectExec("update `shared_links` set `opens`").WillReturnResult(sqlmock.NewResult(0, 1))
	linkRating("g1", true)
	mock.ExpectQuery("select `rating` from `image_groups`").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"rating"}).AddRow(false))
	if g, err := mg.OpenSharedGallery(id, secret); err != nil || g.Rating || g.Items[1].Stars != 0 || g.Items[1].Comment != "" {
		t.Fatalf("rating off: %+v %v", g, err)
	}
	alive()
	linkRating("g1", true)
	target(files[1].Hash, false, true)
	if _, err := mg.RateSharedGalleryItem(id, secret, 1, 3, ""); !errors.Is(err, ErrRatingLinkOff) {
		t.Errorf("rating off: %v", err)
	}
	// The photo taken out of the collection.
	alive()
	linkRating("g1", true)
	target(files[1].Hash, true, false)
	if _, err := mg.RateSharedGalleryItem(id, secret, 1, 3, ""); !errors.Is(err, ErrNotInCollection) {
		t.Errorf("taken out: %v", err)
	}
	// Refused before the database: stars out of range, a comment too long.
	alive()
	if _, err := mg.RateSharedGalleryItem(id, secret, 1, 9, ""); !errors.Is(err, ErrRatingStars) {
		t.Errorf("9 stars: %v", err)
	}
	// What doesn't open is the gallery's one answer.
	alive()
	if _, err := mg.RateSharedGalleryItem(id, secret, 2, 3, ""); !errors.Is(err, ErrNoSuchGallery) {
		t.Errorf("an index past the end: %v", err)
	}
	alive()
	if _, err := mg.RateSharedGalleryItem(id, strings.Repeat("0", 64), 0, 3, ""); !errors.Is(err, ErrNoSuchGallery) {
		t.Errorf("a wrong secret: %v", err)
	}
	if _, err := mg.RateSharedGalleryItem("../../etc", secret, 0, 3, ""); !errors.Is(err, ErrNoSuchGallery) {
		t.Errorf("a bad id: %v", err)
	}
}

// Galleries made without Rating - and those made before it, with no key or
// hashes - take no ratings and read nothing more when opened.
func TestUnratedSharedGalleryTakesNoRating(t *testing.T) {
	_, ses := galleryTestEnv(t)
	files := []*pb.File{libraryFile(t, ses, "plain-1.jpg", "image/jpeg", []byte("JPEG"))}
	mg, mock := ratingMock(t)
	mock.ExpectExec("insert into `shared_links` \\(`uuid`, `size`, `created`, `kind`, `description`, `files`, `expires`\\) values").
		WillReturnResult(sqlmock.NewResult(1, 1))
	link, err := mg.buildSharedGallery(ses, files, "", time.Hour, "cala.off-the.cloud", false, nil, &galleryJob{state: &pb.SharedGalleryJob{}})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(strings.TrimPrefix(link, "https://cala.off-the.cloud/shared#"), ".", 2)
	alive := func() {
		mock.ExpectQuery("select `created`, `expires` from `shared_links`").
			WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now(), time.Now().Add(time.Hour)))
	}
	alive()
	mock.ExpectExec("update `shared_links` set `opens`").WillReturnResult(sqlmock.NewResult(0, 1))
	if g, err := mg.OpenSharedGallery(parts[0], parts[1]); err != nil || g.Rating {
		t.Fatalf("%+v %v", g, err)
	}
	alive()
	if _, err := mg.RateSharedGalleryItem(parts[0], parts[1], 0, 3, ""); !errors.Is(err, ErrRatingLinkOff) {
		t.Errorf("rated: %v", err)
	}
}

// A rated collection's search: the filter and order go to the query, the
// date cut doesn't (a ranked search), and each photo of the page carries
// its rating. A collection whose Rating is off is searched by date, its
// ratings neither filtered on nor read.
func TestImageSearchRated(t *testing.T) {
	_, ses := galleryTestEnv(t)
	orig := maxImagesSearch
	maxImagesSearch = func() int { return 10 }
	t.Cleanup(func() { maxImagesSearch = orig })
	mg, mock := ratingMock(t)
	mock.MatchExpectationsInOrder(false)
	hash := func(i int) string { return fmt.Sprintf("%064x", 0x5a7000+i) }
	for i := range 3 {
		if err := blobstore.WriteBytes(thumbnailPath(hash(i)), ses, []byte("thumb")); err != nil {
			t.Fatal(err)
		}
	}
	rows := func(is ...int) *sqlmock.Rows {
		r := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
		for _, i := range is {
			r.AddRow(hash(i), "image/jpeg", time.Now(), time.Now(), fmt.Sprintf("/p/%d.jpg", i), 1)
		}
		return r
	}
	before := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("select `rating` from `image_groups` where `id` = \\?").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"rating"}).AddRow(true))
	mock.ExpectQuery("left join `photo_ratings` as `pr` .*coalesce\\(`pr`.`stars`, 0\\) >= 4 order by coalesce\\(`pr`.`stars`, 0\\) desc").
		WithArgs("g1").WillReturnRows(rows(2, 0))
	expectCopies(mock, 1)
	mock.ExpectQuery("from `photo_ratings` where `group_id` = \\? and `hash` in").WithArgs("g1", hash(2), hash(0)).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "stars", "comment", "shared_comment", "link", "updated"}).
			AddRow(hash(2), 5, nil, nil, nil, time.Now()).AddRow(hash(0), 4, nil, nil, "link-1", time.Now()))
	files, token, err := mg.ImageSearchRated(&dao.RatingQuery{MinStars: 4, ByRating: true}, "", nil, ses, "", nil, "", false, nil, "g1", &before, nil, 0, 0, false, false)
	if err != nil || token != "" || len(files) != 2 {
		t.Fatalf("%v %q %v", files, token, err)
	}
	if files[0].Hash != hash(2) || files[0].Rating.GetStars() != 5 || files[1].Rating.GetStars() != 4 || !files[1].Rating.GetFromLink() {
		t.Errorf("page %+v", files)
	}

	mock.ExpectQuery("select `rating` from `image_groups` where `id` = \\?").WithArgs("g2").WillReturnRows(sqlmock.NewRows([]string{"rating"}).AddRow(false))
	mock.ExpectQuery("and `ig`.`group_id` = \\? where `f`.`created` <= \\? order by `f`.`created` desc").
		WithArgs("g2", sqlmock.AnyArg()).WillReturnRows(rows(1))
	expectCopies(mock, 1)
	files, _, err = mg.ImageSearchRated(&dao.RatingQuery{MinStars: 4, ByRating: true}, "", nil, ses, "", nil, "", false, nil, "g2", &before, nil, 0, 0, false, false)
	if err != nil || len(files) != 1 || files[0].Rating != nil {
		t.Fatalf("rating off: %v %v", files, err)
	}
}

// Sharing a collection with "Let people rate the photos" readies it - the
// key pair, its link key, its Rating turned on - before the copy starts,
// and the gallery records the collection. The same switch on a selection
// is ignored.
func TestStartRatedSharedGallery(t *testing.T) {
	_, ses := galleryTestEnv(t)
	photo := libraryFile(t, ses, "share-rated.jpg", "image/jpeg", []byte("JPEG"))
	mg, mock := ratingMock(t)
	v := newRatingVault(t, ses)
	key := bytes.Repeat([]byte{4}, 32)

	mock.ExpectQuery("join `image_group_files` as `ig` on `ig`.`hash` = `f`.`hash` and `ig`.`group_id` = \\?").WithArgs("g1").
		WillReturnRows(fileRows(photo))
	v.expect(mock)
	mock.ExpectQuery("select `rating_key` from `image_groups`").WithArgs("g1").
		WillReturnRows(sqlmock.NewRows([]string{"rating_key"}).AddRow(ses.Encrypt(key)))
	mock.ExpectQuery("`comment` is not null and `shared_comment` is null").WithArgs("g1").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "stars", "comment", "shared_comment", "link", "updated"}))
	mock.ExpectQuery("select `rating` from `image_groups` where `id` = \\?").WithArgs("g1").WillReturnRows(sqlmock.NewRows([]string{"rating"}).AddRow(false))
	mock.ExpectExec("update `image_groups` set `rating` = \\? where `id` = \\?").WithArgs(true, "g1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("insert into `shared_links` \\(`uuid`, `size`, `created`, `kind`, `description`, `files`, `expires`, `group_id`, `rating`\\)").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 1, sqlmock.AnyArg(), "g1").WillReturnResult(sqlmock.NewResult(1, 1))
	job, err := mg.StartRatedSharedGallery(ses, &pb.SharedGallerySource{GroupId: "g1"}, "", 24, "kim.off-the.cloud", false, true)
	if err != nil {
		t.Fatal(err)
	}
	waitGalleryJob(t, job.JobId)
	if j, _ := SharedGalleryJobState(job.JobId, ""); j.Error != "" || j.Link == "" {
		t.Fatalf("job %+v", j)
	}

	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").
		WithArgs(photo.Path).WillReturnRows(fileRows(photo))
	mock.ExpectExec("insert into `shared_links` \\(`uuid`, `size`, `created`, `kind`, `description`, `files`, `expires`\\) values").
		WillReturnResult(sqlmock.NewResult(1, 1))
	job, err = mg.StartRatedSharedGallery(ses, &pb.SharedGallerySource{Paths: []string{photo.Path}}, "", 24, "kim.off-the.cloud", false, true)
	if err != nil {
		t.Fatal(err)
	}
	waitGalleryJob(t, job.JobId)
}
