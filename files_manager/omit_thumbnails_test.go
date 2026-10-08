// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
)

// withDefaultPage makes the device's default page size n for the test (the
// test config's is 2).
func withDefaultPage(t *testing.T, n int) {
	t.Helper()
	orig := maxImagesSearch
	maxImagesSearch = func() int { return n }
	t.Cleanup(func() { maxImagesSearch = orig })
}

// SearchPhotos.omit_thumbnails: the rows come without content, and #147 is
// checked by a stat, never a read - so a photo whose thumbnail is there
// but can't be read (d) is listed, where a page with thumbnails leaves it
// out, and a photo with no thumbnail (e) or only a small one (c, a delete
// half done) is left out as ever. With small_thumbnails each row says
// whether the small one is there now, and a missing one is queued, so the
// next page says it is.
func TestImageSearchOmittingThumbnails(t *testing.T) {
	_, ses := galleryTestEnv(t)
	quietQueue(t)
	withDefaultPage(t, 30)
	f := newSmallFixture(t, ses, 0xfa000)
	d, e := fmt.Sprintf("%064x", 0xfa003), fmt.Sprintf("%064x", 0xfa004)
	withBlob(t, d)
	withBlob(t, e)
	// Not sealed: any read of them fails.
	for _, p := range []string{thumbnailPath(d), smallThumbnailPath(d)} {
		if err := os.WriteFile(p, []byte("not a sealed thumbnail"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hashes := []string{f.a, f.b, f.c, d, e}
	db, mock, _ := sqlmock.New()
	defer db.Close()
	for i := 0; i < 5; i++ {
		rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
		for i, h := range hashes {
			rows.AddRow(h, "image/jpeg", time.Now(), time.Now(), fmt.Sprintf("/p/%d.jpg", i), 1)
		}
		mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(rows)
	}
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})
	search := func(small, omit bool) string {
		t.Helper()
		files, _, err := mg.ImageSearch(ses, "", nil, "", false, nil, "", nil, 0, 0, small, omit)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, file := range files {
			if omit && file.Content != nil {
				t.Errorf("%s came with content", file.Path)
			}
			if file.Hash == "" || file.Mime != "image/jpeg" || file.Created == nil || dao.FileSize(file) != 1 {
				t.Errorf("%s came without its row: %+v", file.Path, file)
			}
			if file.ThumbnailSmall == nil {
				t.Errorf("%s doesn't say which thumbnail it stands for", file.Path)
			}
			got = append(got, fmt.Sprintf("%s:%v", strings.TrimSuffix(strings.TrimPrefix(file.Path, "/p/"), ".jpg"), file.GetThumbnailSmall()))
		}
		return strings.Join(got, " ")
	}
	expect := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got [%s], want [%s]", what, got, want)
		}
	}

	expect("omitted, big ones", search(false, true), "0:false 1:false 3:false")
	if _, err := os.Stat(smallThumbnailPath(f.b)); err == nil {
		t.Fatal("b's small thumbnail was made, though no small one was asked for")
	}
	expect("omitted, small ones", search(true, true), "0:true 1:false 3:true")
	waitForFile(t, smallThumbnailPath(f.b))
	waitQueueIdle(t, mg)
	expect("omitted, b's small one made since", search(true, true), "0:true 1:true 3:true")
	// With thumbnails, d's can't be read: left out, as before.
	expect("with thumbnails", search(true, false), "0:true 1:true")
	expect("with big thumbnails", search(false, false), "0:false 1:false")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A page without thumbnails may be larger than the default, up to
// cMaxPageWithoutThumbnails; one with them never is. 0 or less is the
// default either way, and a default over the cap stays the ceiling.
func TestPageSize(t *testing.T) {
	withDefaultPage(t, 30)
	for _, c := range []struct {
		limit int32
		omit  bool
		want  int
	}{
		{0, false, 30}, {12, false, 12}, {30, false, 30}, {100, false, 30}, {-5, false, 30},
		{0, true, 30}, {12, true, 12}, {100, true, 100}, {200, true, 200}, {500, true, 200}, {-5, true, 30},
	} {
		if got := pageSize(c.limit, c.omit); got != c.want {
			t.Errorf("limit %d, omit %v: %d photos, want %d", c.limit, c.omit, got, c.want)
		}
	}
	withDefaultPage(t, 300)
	if got := pageSize(500, true); got != 300 {
		t.Errorf("a default of 300: a page without thumbnails of %d, want 300", got)
	}
	if got := pageSize(250, true); got != 250 {
		t.Errorf("a default of 300: limit 250 gave %d", got)
	}
}

// One token's pages may mix both kinds and sizes: every photo once, in
// order, with content exactly on the pages that ask for it.
func TestImageSearchPagesMixingOmittedThumbnails(t *testing.T) {
	_, ses := galleryTestEnv(t)
	withDefaultPage(t, 30)
	const total = 250
	hash := func(i int) string { return fmt.Sprintf("%064x", 0xfb000+i) }
	for i := 0; i < total; i++ {
		if err := blobstore.WriteBytes(thumbnailPath(hash(i)), ses, []byte(fmt.Sprint("thumb-", i))); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(thumbnailPath(hash(i))) })
	}
	db, mock, _ := sqlmock.New()
	defer db.Close()
	for search := 0; search < 2; search++ {
		rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
		for i := 0; i < total; i++ {
			rows.AddRow(hash(i), "image/jpeg", time.Now(), time.Now(), fmt.Sprintf("/p/%d.jpg", i), 1)
		}
		mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(rows)
	}
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})

	next := 0
	token := ""
	have := int32(0)
	for i, page := range []struct {
		limit int32
		omit  bool
		size  int
	}{{12, true, 12}, {0, false, 30}, {500, true, 200}, {500, false, 8}} {
		files, tok, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", nil, have, page.limit, false, page.omit)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != page.size {
			t.Fatalf("page %d (limit %d, omit %v): %d photos, want %d", i, page.limit, page.omit, len(files), page.size)
		}
		for _, f := range files {
			if f.Path != fmt.Sprintf("/p/%d.jpg", next) {
				t.Fatalf("page %d: %s where /p/%d.jpg was due", i, f.Path, next)
			}
			if want := fmt.Sprint("thumb-", next); page.omit != (f.Content == nil) || (!page.omit && string(f.Content) != want) {
				t.Errorf("page %d, %s: content %q (omit %v)", i, f.Path, f.Content, page.omit)
			}
			next++
		}
		have += int32(len(files))
		if token = tok; (token == "") != (i == 3) {
			t.Fatalf("page %d: token %q", i, token)
		}
	}
	// A limit over the default with thumbnails: the default, as before.
	if files, _, _ := mg.ImageSearch(ses, "", nil, "", false, nil, "", nil, 0, 100, false, false); len(files) != 30 {
		t.Errorf("limit 100 with thumbnails: %d photos, want 30", len(files))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// GetThumbnails looks at MaxThumbnailsPerRequest paths at most and stops
// before the byte cap, saying where (ListOfFiles.ask_again_from): those
// paths are asked for again, not taken for paths without a thumbnail. A
// path left out before it has none.
func TestThumbnailsSayWhereTheyStopped(t *testing.T) {
	_, ses := galleryTestEnv(t)
	// Small ones are asked for and there are none: each answer queues a
	// fix, whose worker must not outlive the test.
	quietQueue(t)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	cols := []string{"hash", "mime", "created", "modified", "path", "size"}
	mg := &Manager{dao: dao.NewWithDB(db)}

	// More paths than a request takes: documents, none answered.
	var paths []string
	for i := 0; i < MaxThumbnailsPerRequest+2; i++ {
		paths = append(paths, fmt.Sprintf("/d/%d.pdf", i))
	}
	for _, p := range paths[:MaxThumbnailsPerRequest] {
		mock.ExpectQuery("from `files` where `path` = \\?").WithArgs(p).
			WillReturnRows(sqlmock.NewRows(cols).AddRow(strings.Repeat("d", 64), "application/pdf", time.Now(), time.Now(), p, 1))
	}
	files, from := mg.Thumbnails(ses, paths, true)
	if len(files) != 0 || from != MaxThumbnailsPerRequest {
		t.Errorf("%d paths: %d answered, ask again from %d, want 0 and %d", len(paths), len(files), from, MaxThumbnailsPerRequest)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// The byte cap: three thumbnails of 3 MB fit two to an answer.
	big := make([]byte, 3<<20)
	rand.Read(big)
	hash := func(i int) string { return fmt.Sprintf("%064x", 0xfc000+i) }
	for i := 1; i <= 3; i++ {
		if err := blobstore.WriteBytes(thumbnailPath(hash(i)), ses, big); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(thumbnailPath(hash(i))) })
	}
	row := func(p string, i int) {
		mock.ExpectQuery("from `files` where `path` = \\?").WithArgs(p).
			WillReturnRows(sqlmock.NewRows(cols).AddRow(hash(i), "image/jpeg", time.Now(), time.Now(), p, 1))
	}
	mock.ExpectQuery("from `files` where `path` = \\?").WithArgs("/gone.jpg").WillReturnRows(sqlmock.NewRows(cols))
	row("/p/1.jpg", 1)
	row("/p/2.jpg", 2)
	row("/p/3.jpg", 3)
	files, from = mg.Thumbnails(ses, []string{"/gone.jpg", "/p/1.jpg", "/p/2.jpg", "/p/3.jpg"}, true)
	if len(files) != 2 || files[0].Path != "/p/1.jpg" || files[1].Path != "/p/2.jpg" || from != 3 {
		t.Fatalf("over the cap: %d answered, ask again from %d, want /p/1.jpg and /p/2.jpg, then from 3", len(files), from)
	}
	for _, f := range files {
		if !bytes.Equal(f.Content, big) || f.ThumbnailSmall == nil || f.GetThumbnailSmall() || f.Hash == "" {
			t.Errorf("%s: not its big thumbnail, marked as such, with its hash", f.Path)
		}
	}
	row("/p/3.jpg", 3)
	files, from = mg.Thumbnails(ses, []string{"/p/3.jpg"}, true)
	if len(files) != 1 || from != 0 {
		t.Errorf("asked again: %d answered, ask again from %d", len(files), from)
	}
	waitQueueIdle(t, mg)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
