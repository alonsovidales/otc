// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
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
		mg := &Manager{dao: dao.NewWithDB(db)}
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
