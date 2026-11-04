// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
)

func TestValidNewPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/Photos/2024/a.jpg":                   true,
		"/a":                                   true,
		"/ios/0F3A/IMG_0001.HEIC":              true,
		"/mac/Kim’s_MacBook/Users/k/a b.txt":   true,
		"/linux/pc/home/k/notes.txt ":          true, // a name may end in a space
		"/a/.hidden":                           true,
		"/a/..b":                               true,
		"/windows/PC/C/Users/k/report (1).doc": true,
		"":                                     false,
		"a.jpg":                                false,
		"Photos/a.jpg":                         false,
		"/":                                    false,
		"/a/":                                  false,
		"/a//b.jpg":                            false,
		"//a":                                  false,
		"/a/./b":                               false,
		"/a/../b":                              false,
		"/..":                                  false,
		"/a/..":                                false,
		"/a\x00b":                              false,
	} {
		if got := validNewPath(p); got != want {
			t.Errorf("validNewPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// noRowAt mocks GetFileByPath finding nothing at path.
func noRowAt(mock sqlmock.Sqlmock, path string) {
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs(path).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}))
}

// rowAt mocks GetFileByPath's = finding the row stored at stored, with
// hash.
func rowAt(mock sqlmock.Sqlmock, asked, stored, hash string) {
	now := time.Now()
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs(asked).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow(hash, "text/plain", now, now, stored, 7))
}

// No new file is stored where none may be (validNewPath) - by an upload, a
// chunked upload or a link - and it is refused before anything is
// written; a file stored at such a path before can still be linked over.
func TestNewPathsAreChecked(t *testing.T) {
	_, ses := galleryTestEnv(t)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
	const bad = "/a/../../.bashrc"
	content := []byte("export PATH=/tmp")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	for range 3 {
		noRowAt(mock, bad)
	}
	var refused *BadPathError
	if _, err := mg.UploadFile(ses, bad, content, true, nil, nil, ""); !errors.As(err, &refused) {
		t.Errorf("UploadFile: %v", err)
	} else if want := "no file can be saved as /a/../../.bashrc: a path starts with / and none of the names in it is empty, . or .."; err.Error() != want || devtext.Text("en", err) != want {
		t.Errorf("its English: %q", err.Error())
	} else if es := devtext.Text("es", err); es == want || !strings.Contains(es, bad) {
		t.Errorf("its Spanish: %q", es)
	}
	if _, err := os.Stat(blobPath(hash)); !os.IsNotExist(err) {
		os.Remove(blobPath(hash))
		t.Error("the refused upload's content was written")
	}
	if _, _, err := mg.BeginUpload(ses, bad, int64(len(content)), false, nil, nil, "", "", nil); !errors.As(err, &refused) {
		t.Errorf("BeginUpload: %v", err)
	}
	if _, err := mg.LinkFile(ses, bad, hash, false, nil, nil, ""); !errors.As(err, &refused) {
		t.Errorf("LinkFile: %v", err)
	}

	// Stored before the check: linked to again as any path is.
	const old = "/old//report.txt"
	linked := strings.Repeat("c", 64)
	if err := os.WriteFile(blobPath(linked), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(linked))
	rowAt(mock, old, old, linked)
	now := time.Now()
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = \\?").WithArgs(linked).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow(linked, "text/plain", now, now, old, 7))
	mock.ExpectExec("insert into `files`").WillReturnError(&mysql.MySQLError{Number: 1062})
	rowAt(mock, old, old, linked)
	if f, err := mg.LinkFile(ses, old, linked, false, nil, nil, ""); err != nil || f.Path != old {
		t.Errorf("a path stored before: %+v, %v", f, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// "notes.txt " and "notes.txt" are one key to the database: an upload of
// the one where the other is is refused, saying why - it used to replace
// the other's content (a sync client's edit) - and leaves nothing behind;
// a link too.
func TestNameDifferingBySpacesIsRefused(t *testing.T) {
	_, ses := galleryTestEnv(t)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
	const there, spaced = "/Docs/notes.txt", "/Docs/notes.txt "
	content := []byte("the notes, edited on a Mac")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	referenced := func(n int) {
		mock.ExpectQuery("select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`").
			WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(n))
	}
	taken := func() {
		mock.ExpectExec("insert into `files`").WillReturnError(&mysql.MySQLError{Number: 1062})
		rowAt(mock, spaced, there, strings.Repeat("e", 64))
		mock.ExpectQuery("select `path` from `files` where `path` = \\?").WithArgs(spaced).
			WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow(there))
	}
	referenced(0) // contentKnown
	taken()
	referenced(0) // the written content, unused: removed
	var spaces *SpacesNameError
	_, err := mg.UploadFile(ses, spaced, content, true, nil, nil, "")
	if !errors.As(err, &spaces) {
		t.Fatalf("UploadFile: %v", err)
	}
	if want := "\"notes.txt \" can't be saved: a file here has the same name with or without spaces at its end, and the device keeps only one of them - rename one"; err.Error() != want {
		t.Errorf("its English: %q", err.Error())
	}
	if _, err := os.Stat(blobPath(hash)); !os.IsNotExist(err) {
		os.Remove(blobPath(hash))
		t.Error("the refused upload's content stayed on the disk")
	}

	linked := strings.Repeat("c", 64)
	if err := os.WriteFile(blobPath(linked), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(linked))
	now := time.Now()
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = \\?").WithArgs(linked).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow(linked, "text/plain", now, now, "/x.txt", 7))
	taken()
	if _, err := mg.LinkFile(ses, spaced, linked, true, nil, nil, ""); !errors.As(err, &spaces) {
		t.Errorf("LinkFile: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // an override would show up here
	}
}

func TestZipEntryName(t *testing.T) {
	for _, c := range []struct{ path, prefix, want string }{
		{"/a/b/c.jpg", "/a/b/", "c.jpg"},
		{"/a/b/c.jpg", "/a/", "b/c.jpg"},
		{"/a/../../.bashrc", "/a/", ".bashrc"},
		{"/a/./b//c.txt", "/a/", "b/c.txt"},
		{`/a/..\..\x.txt`, "/a/", ".._.._x.txt"},
		{"/a/..", "/a/", "file"},
	} {
		if got := zipEntryName(c.path, c.prefix); got != c.want {
			t.Errorf("zipEntryName(%q, %q) = %q, want %q", c.path, c.prefix, got, c.want)
		}
	}
}

// A share archive's entries stay inside the folder it is unpacked to,
// whatever paths were stored before they were checked.
func TestShareArchiveEntriesStayInside(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	files := []*pb.File{
		libraryFile(t, ses, "notes.md", "text/markdown", []byte("notes")),
		libraryFile(t, ses, "x", "text/plain", []byte("echo")),
	}
	files[0].Path, files[1].Path = "/a/notes.md", "/a/../../.bashrc"
	db, mock, _ := sqlmock.New()
	defer db.Close()
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs(f.Path).
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
				AddRow(f.Hash, f.Mime, time.Now(), time.Now(), f.Path, f.Size))
	}
	mock.ExpectExec("insert into `shared_links`").WillReturnResult(sqlmock.NewResult(1, 1))
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), sharedLinkTTL: time.Hour})
	link, err := mg.GetSharedLink(ses, paths, "kim.off-the.cloud")
	if err != nil {
		t.Fatal(err)
	}
	id, secret, _ := strings.Cut(strings.TrimPrefix(link, "https://kim.off-the.cloud/"+CDownloadAttr), "_")
	defer os.Remove(filepath.Join(storage, id))
	raw, err := blobstore.ReadAll(filepath.Join(storage, id), linkKey(secret))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	if want := []string{".bashrc", "notes.md"}; !slices.Equal(names, want) {
		t.Errorf("entries %q, want %q", names, want)
	}
}
