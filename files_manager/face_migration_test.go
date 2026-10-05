// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
)

// The legacy sweep reads the faces a page at a time, rewrites only the
// plaintext rows, and once a pass has succeeded later sign-ins don't run
// it again - while a pass that failed is retried at the next one.
func TestMigrateLegacyFaceEncryptionRunsOncePagedAndRetriesAFailure(t *testing.T) {
	_, ses := galleryTestEnv(t)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}
	page := func(after string, rows *sqlmock.Rows) {
		mock.ExpectQuery("select `id`, `embedding`, `thumbnail` from `faces` where `id` > \\? order by `id` limit \\?").
			WithArgs(after, cFaceMigrationPage).WillReturnRows(rows)
	}
	full := sqlmock.NewRows([]string{"id", "embedding", "thumbnail"})
	for i := 0; i < cFaceMigrationPage; i++ {
		full.AddRow(fmt.Sprintf("f%03d", i), ses.Encrypt([]byte("emb")), ses.Encrypt([]byte("thumb")))
	}
	last := fmt.Sprintf("f%03d", cFaceMigrationPage-1)

	// First sign-in: two pages; the plaintext row's rewrite fails.
	page("", full)
	page(last, sqlmock.NewRows([]string{"id", "embedding", "thumbnail"}).AddRow("g000", []byte("plain"), []byte("plain")))
	mock.ExpectExec("update `faces` set `embedding`").WillReturnError(errors.New("db hiccup"))
	mg.MigrateLegacyFaceEncryption(ses)
	if mg.faceMigDone.Load() {
		t.Fatal("a pass that failed was taken as done")
	}

	// The next sign-in retries, and succeeds.
	page("", sqlmock.NewRows([]string{"id", "embedding", "thumbnail"}).AddRow("g000", []byte("plain"), []byte("plain")))
	mock.ExpectExec("update `faces` set `embedding`").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "g000").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mg.MigrateLegacyFaceEncryption(ses)
	if !mg.faceMigDone.Load() {
		t.Fatal("a clean pass wasn't recorded")
	}

	// Any later sign-in: nothing at all.
	mg.MigrateLegacyFaceEncryption(ses)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
