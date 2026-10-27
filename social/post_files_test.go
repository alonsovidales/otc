// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// writePostFiles writes hash's media and thumbnail in dir.
func writePostFiles(t *testing.T, dir string, hashes ...string) {
	t.Helper()
	for _, h := range hashes {
		for _, name := range []string{h, h + "_thumbnail"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("plaintext of "+name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func expectInUse(mock sqlmock.Sqlmock, hash string, n int) {
	mock.ExpectQuery("select count\\(\\*\\) from `social_publications_files` where `hash` = \\?").WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(n))
}

// A post that wasn't made takes back the plaintext copies it wrote - but
// not one another post here names.
func TestDiscardRemovesWhatAFailedPostWrote(t *testing.T) {
	dir := t.TempDir()
	mine, shared := hashOf("only this post"), hashOf("an older post's photo too")
	writePostFiles(t, dir, mine, shared)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sc := &Social{dao: dao.NewWithDB(db)}
	held := &postFiles{dir: dir}
	held.hold(mine)
	held.hold(shared)
	held.hold(mine)        // twice in one post
	held.hold("../escape") // never a path
	expectInUse(mock, mine, 0)
	expectInUse(mock, shared, 1)
	held.discard(sc)
	if got := names(t, dir); strings.Join(got, ",") != shared+","+shared+"_thumbnail" {
		t.Fatalf("left %v, want only the shared photo's files", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if n := len(postsInFlight.n); n != 0 {
		t.Fatalf("%d hashes still held", n)
	}
}

// Two posts of one photo at once: the one that fails leaves the file the
// other is about to name.
func TestDiscardLeavesWhatAnotherPostHolds(t *testing.T) {
	dir := t.TempDir()
	h := hashOf("posted twice at once")
	writePostFiles(t, dir, h)
	db, _, _ := sqlmock.New()
	defer db.Close()
	sc := &Social{dao: dao.NewWithDB(db)}
	failing, other := &postFiles{dir: dir}, &postFiles{dir: dir}
	failing.hold(h)
	other.hold(h)
	failing.discard(sc) // no query: the other post holds it
	if got := names(t, dir); len(got) != 2 {
		t.Fatalf("left %v", got)
	}
	other.release()
	if n := len(postsInFlight.n); n != 0 {
		t.Fatalf("%d hashes still held", n)
	}
}

// The start-up sweep removes old post files no post names - media and
// thumbnails - and nothing else: a post's file, one written within the
// day, one a post being made holds, one a post was stored with since the
// list was read, and anything that isn't a post file stay.
func TestSweepPostFiles(t *testing.T) {
	dir := t.TempDir()
	used, unused, recent, held, since := hashOf("used"), hashOf("unused"), hashOf("recent"), hashOf("held"), hashOf("since")
	writePostFiles(t, dir, used, unused, recent, held, since)
	others := []string{".pub-123", ".post-456.mp4", "notes.txt", strings.Repeat("a", 63), "x_thumbnail"}
	for _, n := range others {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, e := range names(t, dir) {
		if !strings.HasPrefix(e, recent) {
			os.Chtimes(filepath.Join(dir, e), old, old)
		}
	}
	p := &postFiles{dir: dir}
	p.hold(held)
	defer p.release()
	stillUsed := func(h string) (bool, error) { return h == since, nil }
	n, freed := sweepPostFiles(dir, map[string]bool{used: true}, time.Now().Add(-cUnusedPostFileAge), stillUsed)
	if n != 2 || freed == 0 {
		t.Fatalf("removed %d (%d bytes), want the unused media and thumbnail", n, freed)
	}
	for _, e := range names(t, dir) {
		if strings.HasPrefix(e, unused) {
			t.Errorf("%s is still there", e)
		}
	}
	want := 2*4 + len(others)
	if got := names(t, dir); len(got) != want {
		t.Fatalf("%d files left, want %d: %v", len(got), want, got)
	}
}

// cPostsChildEnv marks the child process the NewPublication and
// GetPublicationMedia tests run in: they need [otc] storage-path and
// unenc-storage-path, and with an [otc] section loaded the other tests of
// this package would behave otherwise (cfg can't be unloaded).
const cPostsChildEnv = "OTC_SOCIAL_POSTS_CHILD"

// inChild runs the calling test in a child process with a config; true in
// the parent, which then returns.
func inChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv(cPostsChildEnv) == "1" {
		return false
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), cPostsChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("in its own process: %v\n%s", err, out)
	}
	return true
}

// postsEnv loads a config with a storage and a posts folder and gives a
// signed-in session (its vault in a mocked database).
func postsEnv(t *testing.T) (storage, unenc string, ses *session.Session) {
	t.Helper()
	dir := t.TempDir()
	storage, unenc = filepath.Join(dir, "storage"), filepath.Join(dir, "unenc")
	os.MkdirAll(storage, 0o750)
	os.MkdirAll(unenc, 0o750)
	os.MkdirAll(filepath.Join(dir, "etc"), 0o750)
	ini := "[otc]\nstorage-path=" + storage + "/\nunenc-storage-path=" + unenc + "/\n"
	if err := os.WriteFile(filepath.Join(dir, "etc", "otc_poststest.ini"), []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	os.Chdir(dir)
	err := cfg.Init("otc", "poststest")
	os.Chdir(wd)
	if err != nil {
		t.Fatal(err)
	}
	sesDB, sesMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sesDB.Close() })
	sesMock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	sesMock.ExpectExec("insert into `vault`").WillReturnResult(sqlmock.NewResult(1, 1))
	if ses, err = session.New("owner-uuid", "test-password", true, dao.NewWithDB(sesDB)); err != nil {
		t.Fatal(err)
	}
	return storage, unenc, ses
}

// A post that fails after its photo's copies were written - here its
// event can't be stored - takes them back: the photo doesn't stay in the
// posts' folder unencrypted for good. One that is made keeps them.
func TestNewPublicationTakesBackWhatItWrote(t *testing.T) {
	if inChild(t) {
		return
	}
	storage, unenc, ses := postsEnv(t)
	photo := []byte("\xff\xd8\xff\xe0 the owner's photo")
	hash := hashOf(string(photo))
	if err := blobstore.WriteBytes(filepath.Join(storage, hash), ses, photo); err != nil {
		t.Fatal(err)
	}
	if err := blobstore.WriteBytes(filepath.Join(storage, hash+"_thumbnail"), ses, []byte("thumb")); err != nil {
		t.Fatal(err)
	}
	row := func(mock sqlmock.Sqlmock) {
		now := time.Now()
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = ?").
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
				AddRow(hash, "image/jpeg", now, now, "/photos/a.jpg", len(photo)))
	}
	for _, made := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		d := dao.NewWithDB(db)
		sc := &Social{dao: d, filesmanager: filesmanager.NewWithDAO(d), profile: profile.InitFromPb(d, &pb.Profile{Domain: "kim.off-the.cloud"})}
		row(mock) // WaitForContent
		row(mock) // publishFile
		if made {
			mock.ExpectExec("insert into `events`").WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectBegin()
			mock.ExpectExec("insert into `social_publications`").WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec("insert into `social_publications_files`").WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
		} else {
			mock.ExpectExec("insert into `events`").WillReturnError(errors.New("the database is gone"))
			expectInUse(mock, hash, 0)
		}
		_, err = sc.NewPublication(ses, "a post", []string{"/photos/a.jpg"}, nil)
		if made != (err == nil) {
			t.Fatalf("made %v: %v", made, err)
		}
		got := names(t, unenc)
		if !made && len(got) != 0 {
			t.Fatalf("a post that failed left %v", got)
		}
		if made && strings.Join(got, ",") != hash+","+hash+"_thumbnail" {
			t.Fatalf("a post that was made has %v", got)
		}
		if b, _ := os.ReadFile(filepath.Join(unenc, hash)); made && string(b) != string(photo) {
			t.Fatalf("the post's copy: %q", b)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("made %v: %v", made, err)
		}
		db.Close()
	}
	if n := len(postsInFlight.n); n != 0 {
		t.Fatalf("%d hashes still held", n)
	}
}

// A post's file too large to send whole is refused - "too_large": the
// apps stream a video, a friend's device keeps the thumbnail - before it
// is read; a smaller one comes whole.
func TestGetPublicationMediaRefusesWhatItCantSendWhole(t *testing.T) {
	if inChild(t) {
		return
	}
	_, unenc, _ := postsEnv(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)
	sc := &Social{dao: d, filesmanager: filesmanager.NewWithDAO(d)}
	limit := sc.filesmanager.MaxPublicationMedia()
	small, big := hashOf("small"), hashOf("big")
	if err := os.WriteFile(filepath.Join(unenc, small), []byte("a small video"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(unenc, big))
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(limit + 1) // sparse: nothing written
	f.Close()
	for _, h := range []string{small, big} {
		mock.ExpectQuery("select `mime` from `social_publications_files`").WithArgs("p1", h).
			WillReturnRows(sqlmock.NewRows([]string{"mime"}).AddRow("video/mp4"))
	}
	content, mime, err := sc.GetPublicationMedia("p1", small)
	if err != nil || string(content) != "a small video" || mime != "video/mp4" {
		t.Fatalf("small: %q, %q, %v", content, mime, err)
	}
	_, _, err = sc.GetPublicationMedia("p1", big)
	if !errors.Is(err, filesmanager.ErrTooLarge) {
		t.Fatalf("big: %v, want ErrTooLarge", err)
	}
	if !strings.Contains(err.Error(), "too large to send whole") {
		t.Errorf("English %q", err.Error())
	}
}
