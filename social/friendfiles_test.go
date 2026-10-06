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
	ok, wrote, err := fr.storeFriendFile("p1", &pb.File{Hash: "../x", Content: []byte("junk")}, dir)
	if err != nil || ok || wrote {
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
	// The friend's word for the size is not what gets counted: the
	// thumbnail and the media as they are on this disk are.
	file := &pb.File{Hash: validHash, Content: []byte("thumb"), Size: 1, Size64: 1 << 40}
	ok, wrote, err := fr.storeFriendFile("p1", file, dir)
	if err != nil || !ok || !wrote {
		t.Fatalf("got %v, %v, %v", ok, wrote, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, validHash+"_thumbnail")); string(b) != "thumb" {
		t.Fatalf("thumbnail not stored: %q", b)
	}
	if n := len("thumb") + len("media"); file.Size64 != int64(n) || file.Size != int32(n) {
		t.Fatalf("size %d (%d), want what is on disk", file.Size64, file.Size)
	}

	mock.ExpectQuery("select count\\(\\*\\) from `social_publications_files` where `hash` = \\?").WithArgs(validHash).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if ok, wrote, err := fr.storeFriendFile("p2", &pb.File{Hash: validHash, Content: []byte("other")}, dir); err != nil || !ok || wrote {
		t.Fatalf("got %v, %v, %v", ok, wrote, err)
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

// A post's file appears under its name only once whole, private, and
// replaces an older copy in one step.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, validHash)
	for _, content := range []string{"first", "second"} {
		if err := writeFileAtomic(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(path); string(b) != content {
			t.Fatalf("got %q, want %q", b, content)
		}
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

// A write cut off by the process dying leaves a temp file that nothing
// else removes or counts: startup removes it, and only it.
func TestRemovePartialWrites(t *testing.T) {
	dir := t.TempDir()
	keep := []string{validHash, validHash + "_thumbnail", ".post-1", ".upload-2"}
	for _, name := range append([]string{".pub-123", ".pub-456"}, keep...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".pub-dir"), 0o700); err != nil {
		t.Fatal(err)
	}

	RemovePartialWrites(dir)

	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := append([]string{".post-1", ".pub-dir", ".upload-2"}, validHash, validHash+"_thumbnail")
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Errorf("left %v, want %v", left, want)
	}
	RemovePartialWrites(filepath.Join(dir, "missing")) // only logged
}
