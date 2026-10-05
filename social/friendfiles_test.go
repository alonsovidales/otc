// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

var validHash = strings.Repeat("ab", 32)

// unencDir is a posts directory with a sibling file a "../" hash would
// reach.
func unencDir(t *testing.T) (dir, outside string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "unenc")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside = filepath.Join(root, "x")
	if err := os.WriteFile(outside, []byte("owner's"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, outside
}

// A friend's hash names files on this disk: one that isn't a content hash
// is left out of the post and nothing is written for it.
func TestStoreFriendFileRefusesAHashThatIsAPath(t *testing.T) {
	dir, outside := unencDir(t)
	fr, mock := friendFrom(t, "x.off-the.cloud")
	ok, err := fr.storeFriendFile("p1", &pb.File{Hash: "../x", Content: []byte("junk")}, dir)
	if err != nil || ok {
		t.Fatalf("got %v, %v; want the file left out", ok, err)
	}
	if _, err := os.Stat(outside + "_thumbnail"); err == nil {
		t.Fatal("a thumbnail was written outside the posts directory")
	}
	if b, _ := os.ReadFile(outside); string(b) != "owner's" {
		t.Fatal("a file outside the posts directory changed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A real hash is stored as before; a thumbnail a post here already uses
// is not replaced by another post naming the same hash.
func TestStoreFriendFileKeepsAThumbnailInUse(t *testing.T) {
	dir, _ := unencDir(t)
	// The media is already here, so nothing is fetched from the friend.
	if err := os.WriteFile(filepath.Join(dir, validHash), []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	fr, mock := friendFrom(t, "x.off-the.cloud")
	ok, err := fr.storeFriendFile("p1", &pb.File{Hash: validHash, Content: []byte("thumb")}, dir)
	if err != nil || !ok {
		t.Fatalf("got %v, %v", ok, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, validHash+"_thumbnail")); string(b) != "thumb" {
		t.Fatalf("thumbnail not stored: %q", b)
	}

	mock.ExpectQuery("select count\\(\\*\\) from `social_publications_files` where `hash` = \\?").WithArgs(validHash).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if ok, err := fr.storeFriendFile("p2", &pb.File{Hash: validHash, Content: []byte("other")}, dir); err != nil || !ok {
		t.Fatalf("got %v, %v", ok, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, validHash+"_thumbnail")); string(b) != "thumb" {
		t.Fatalf("a thumbnail in use was replaced: %q", b)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Rows stored before hashes were checked never become a path to remove.
func TestRemoveUnusedMediaSkipsAHashThatIsAPath(t *testing.T) {
	dir, outside := unencDir(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sc := &Social{dao: dao.NewWithDB(db)}
	if err := os.WriteFile(filepath.Join(dir, validHash), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("select count\\(\\*\\) from `social_publications_files`").WithArgs(validHash).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))

	sc.removeUnusedMedia(dir, []string{"../x", validHash})
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("a file outside the posts directory was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, validHash)); err == nil {
		t.Fatal("unused media was kept")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
