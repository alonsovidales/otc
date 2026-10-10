// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/go-sql-driver/mysql"
)

// expectPathTaken mocks what an upload or link to path meets when path
// already holds other content outside an upload-only folder: the insert
// is a duplicate key, the row there has another hash.
func expectPathTaken(mock sqlmock.Sqlmock, path string) {
	now := time.Now()
	mock.ExpectExec("insert into `files`").WillReturnError(&mysql.MySQLError{Number: 1062})
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs(path).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow(strings.Repeat("e", 64), "text/plain", now, now, path, 5))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
}

// isDuplicatedFile: the refusal is ErrDuplicatedFile, and says exactly
// "Duplicated file" - what the phone apps released before
// error_code duplicated_file match.
func isDuplicatedFile(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, ErrDuplicatedFile) || err.Error() != "Duplicated file" {
		t.Errorf("%s: got %v, want ErrDuplicatedFile", what, err)
	}
}

// A path that already holds other content is refused with
// ErrDuplicatedFile by each way content reaches it without override:
// UploadFile, the chunked upload's FinishUpload, and LinkFile.
func TestTakenPathIsErrDuplicatedFile(t *testing.T) {
	_, ses := galleryTestEnv(t)

	t.Run("UploadFile", func(t *testing.T) {
		content := []byte("an upload to a path that holds something else")
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		// On the disk already: nothing is written, so nothing is removed.
		if err := os.WriteFile(blobPath(hash), []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(blobPath(hash))
		db, mock, _ := sqlmock.New()
		defer db.Close()
		mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
		mock.ExpectQuery("select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`").
			WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		expectPathTaken(mock, "/docs/a.txt")

		f, err := mg.UploadFile(ses, "/docs/a.txt", content, false, nil, nil, "")
		if f != nil {
			t.Errorf("a refused upload answered a file: %+v", f)
		}
		isDuplicatedFile(t, "UploadFile", err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("FinishUpload", func(t *testing.T) {
		content := []byte("a chunked upload to a path that holds something else")
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		defer os.Remove(blobPath(hash))
		db, mock, _ := sqlmock.New()
		defer db.Close()
		mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
		id, err := mg.BeginUpload(ses, "/docs/b.txt", int64(len(content)), false, nil, nil, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mg.UploadChunk(ses, id, 0, content); err != nil {
			t.Fatal(err)
		}
		mock.ExpectQuery("select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`").
			WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		expectPathTaken(mock, "/docs/b.txt")
		// The committed content is still used elsewhere: it stays.
		mock.ExpectQuery("select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`").
			WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))

		f, err := mg.FinishUpload(ses, id, hash)
		if f != nil {
			t.Errorf("a refused upload answered a file: %+v", f)
		}
		isDuplicatedFile(t, "FinishUpload", err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("LinkFile", func(t *testing.T) {
		hash := strings.Repeat("d", 64)
		if err := os.WriteFile(blobPath(hash), []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(blobPath(hash))
		db, mock, _ := sqlmock.New()
		defer db.Close()
		mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
		now := time.Now()
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = \\?").WithArgs(hash).
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
				AddRow(hash, "text/plain", now, now, "/elsewhere/c.txt", 7))
		expectPathTaken(mock, "/docs/c.txt")

		f, err := mg.LinkFile(ses, "/docs/c.txt", hash, false, nil, nil, "")
		if f != nil {
			t.Errorf("a refused link answered a file: %+v", f)
		}
		isDuplicatedFile(t, "LinkFile", err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
}
