// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/go-sql-driver/mysql"
)

// A connection's unfinished uploads go when it closes - their temp files
// too - and nobody else's do.
func TestAbortUploadsOfDropsOnlyThatConnectionsUploads(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	mg := &Manager{}
	connA, connB := new(int), new(int)
	temps := func() int {
		n := 0
		entries, _ := os.ReadDir(storage)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".blob-") {
				n++
			}
		}
		return n
	}
	before := temps()
	idA, err := mg.BeginUpload(ses, "/a.mov", 10, false, nil, nil, "", connA)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := mg.BeginUpload(ses, "/b.mov", 10, false, nil, nil, "", connB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mg.UploadChunk(ses, idA, 0, []byte("12345")); err != nil {
		t.Fatal(err)
	}
	if got := temps(); got != before+2 {
		t.Fatalf("%d temp files, want %d", got, before+2)
	}

	mg.AbortUploadsOf(connA)
	if _, err := mg.UploadChunk(ses, idA, 5, []byte("67890")); err != ErrUnknownUpload {
		t.Errorf("the closed connection's upload is still there: %v", err)
	}
	if _, err := mg.UploadChunk(ses, idB, 0, []byte("12345")); err != nil {
		t.Errorf("another connection's upload was dropped: %v", err)
	}
	if got := temps(); got != before+1 {
		t.Errorf("%d temp files after the abort, want %d", got, before+1)
	}
	mg.AbortUploadsOf(connB)
	if got := temps(); got != before {
		t.Errorf("%d temp files left, want %d", got, before)
	}
}

// Content the device already processed (a photo the phone synced, dropped
// again under another path) isn't queued for processing again; content
// it knows but never processed still is.
func TestUploadOfProcessedContentIsNotProcessedAgain(t *testing.T) {
	_, ses := galleryTestEnv(t)
	for _, processed := range []bool{true, false} {
		content := []byte(fmt.Sprintf("\xff\xd8\xff\xe0 a photo the device has, processed=%v", processed)) // JPEG magic: media
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		if processed {
			if err := os.WriteFile(blobPath(hash)+"_thumbnail", []byte("thumb"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		db, mock, _ := sqlmock.New()
		mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
		mock.ExpectQuery("select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`").
			WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectExec("insert into `files`").WillReturnResult(sqlmock.NewResult(1, 1))
		if !processed {
			mock.ExpectExec("insert ignore into `pending_analysis`").WithArgs(hash, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mg.lanes = &mediaLanes{fast: newLane(0, nil, nil)} // queued, never run
			mg.lanesOnce.Do(func() {})
		}
		if _, err := mg.UploadFile(ses, "/elsewhere/photo.jpg", content, false, nil, nil, ""); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("processed=%v: %v", processed, err)
		}
		if !processed && len(mg.lanes.fast.jobs) != 1 {
			t.Errorf("content never processed wasn't queued")
		}
		if processed && mg.lanes != nil {
			t.Errorf("processed content was queued again")
		}
		os.Remove(blobPath(hash))
		os.Remove(blobPath(hash) + "_thumbnail")
		db.Close()
	}
}

// Overriding a file (the desktop app always uploads with override) in a
// folder that is no longer upload only keeps the versions it had - it
// used to delete them all, and their content.
func TestOverrideKeepsThePathsVersions(t *testing.T) {
	_, ses := galleryTestEnv(t)
	newHash, oldHash := strings.Repeat("1", 64), strings.Repeat("2", 64)
	if err := os.WriteFile(blobPath(newHash), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(newHash))
	cols := []string{"hash", "mime", "created", "modified", "path", "size"}
	now := time.Now()

	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = \\?").WithArgs(newHash).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(newHash, "text/plain", now, now, "/other/a.txt", 7))
	mock.ExpectExec("insert into `files`").WillReturnError(&mysql.MySQLError{Number: 1062})
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/backup/a.txt").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(oldHash, "text/plain", now, now, "/backup/a.txt", 5))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\? for update").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(oldHash))
	mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("select count\\(\\*\\) from `file_versions` where `hash` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1)) // an old version of this path
	mock.ExpectExec("update `files` set").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// The replaced content is still a version's: its blob stays.
	mock.ExpectQuery("select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`").
		WithArgs(oldHash, oldHash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))

	f, err := mg.LinkFile(ses, "/backup/a.txt", newHash, true, nil, nil, "")
	if err != nil || f.Hash != newHash {
		t.Fatalf("LinkFile = %+v, %v", f, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // a delete from file_versions would show up here
	}
}

// Issue #187: a 3 GiB upload or link is stored with its real size, and
// the files it answers with - and the Files grid's - carry it in size64
// and wrapped in size, as before, for the apps that only read that.
func TestLargeSizesReachTheRowAndBothFields(t *testing.T) {
	_, ses := galleryTestEnv(t)
	const size = int64(3) << 30
	const wrapped = int32(-1 << 30)
	hash := strings.Repeat("5", 64)
	if err := os.WriteFile(blobPath(hash), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(hash))
	if err := blobstore.WriteBytes(blobPath(hash)+"_thumbnail", ses, []byte("thumb")); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(hash) + "_thumbnail")
	cols := []string{"hash", "mime", "created", "modified", "path", "size"}
	now := time.Now()

	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
	mock.ExpectExec("insert into `files`").
		WithArgs(hash, "video/mp4", sqlmock.AnyArg(), sqlmock.AnyArg(), "/v.mp4", size, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	f, write, err := mg.registerUpload(ses, "/v.mp4", hash, "video/mp4", size, false, nil, nil, "")
	if err != nil || !write || f.Size64 != size || f.Size != wrapped {
		t.Fatalf("upload: %+v, %v, %v", f, write, err)
	}

	mock.ExpectQuery("from `files` where `hash` = \\?").WithArgs(hash).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(hash, "video/mp4", now, now, "/v.mp4", size))
	mock.ExpectExec("insert into `files`").
		WithArgs(hash, "video/mp4", sqlmock.AnyArg(), sqlmock.AnyArg(), "/copy.mp4", size, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if f, err = mg.LinkFile(ses, "/copy.mp4", hash, false, nil, nil, ""); err != nil || f.Size64 != size || f.Size != wrapped {
		t.Fatalf("link: %+v, %v", f, err)
	}

	mock.ExpectQuery("from `files` where `path` = \\?").WithArgs("/v.mp4").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(hash, "video/mp4", now, now, "/v.mp4", size))
	grid, _ := mg.Thumbnails(ses, []string{"/v.mp4"}, false)
	if len(grid) != 1 || grid[0].Size64 != size || grid[0].Size != wrapped {
		t.Fatalf("grid: %+v", grid)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
