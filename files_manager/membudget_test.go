// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestMemBudgetMakesDownloadsWait(t *testing.T) {
	b := newMemBudget(100)
	first := b.acquire(80)

	done := make(chan struct{})
	released := make(chan struct{})
	go func() {
		release := b.acquire(50) // 80 + 50 > 100: must wait for the first
		close(done)
		release()
		close(released)
	}()
	select {
	case <-done:
		t.Fatal("a download that doesn't fit went ahead anyway")
	case <-time.After(100 * time.Millisecond):
	}

	first()
	first() // releasing twice must not free the budget twice
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the waiting download never started after the first finished")
	}
	<-released
	b.mu.Lock()
	used := b.used
	b.mu.Unlock()
	if used != 0 {
		t.Errorf("budget left at %d, want 0", used)
	}
}

func TestMemBudgetLetsAnOversizedFileRunAlone(t *testing.T) {
	b := newMemBudget(100)
	done := make(chan struct{})
	go func() {
		release := b.acquire(1000) // bigger than the whole budget
		close(done)
		release()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a file bigger than the budget could never be downloaded")
	}
}

// A share-link part reserves the content budget only for a link that
// exists: anyone can name a made-up one, and each used to hold 8 MiB.
func TestSharedLinkAndGalleryKnown(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}

	mock.ExpectQuery("select `created`, `expires` from `shared_links` where `uuid` = \\? and `kind` = 'archive'").WithArgs("real").
		WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now(), nil))
	mock.ExpectQuery("select `created`, `expires` from `shared_links` where `uuid` = \\? and `kind` = 'archive'").WithArgs("made-up").
		WillReturnError(sql.ErrNoRows)
	if !mg.SharedLinkKnown("real") || mg.SharedLinkKnown("made-up") {
		t.Fatal("SharedLinkKnown must follow the shared_links row")
	}

	id, secret := "0b7c6f1e-3a2d-4c5b-8e9f-0123456789ab", strings.Repeat("ab", 32)
	mock.ExpectQuery("select `created`, `expires` from `shared_links` where `uuid` = \\?").WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now(), nil))
	if !mg.SharedGalleryKnown(id, secret) {
		t.Fatal("an existing gallery must be known")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}

	// Malformed ids or secrets never reach the database. This database
	// would answer "the link exists" to the lookup, so a call that asked
	// it would come back true: an unexpected query on its own only gets
	// an error from sqlmock, which reads as "not known" and hides it.
	db2, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db2.Close()
	mock2.MatchExpectationsInOrder(false)
	for range 2 {
		mock2.ExpectQuery("select `created`, `expires` from `shared_links` where `uuid` = \\?").
			WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now(), nil))
	}
	malformed := &Manager{dao: dao.NewWithDB(db2)}
	if malformed.SharedGalleryKnown("not-a-uuid", secret) || malformed.SharedGalleryKnown(id, "short") {
		t.Fatal("a malformed gallery link must not be known, nor looked up")
	}
	if mock2.ExpectationsWereMet() == nil {
		t.Error("a malformed gallery link was looked up in the database")
	}
}

// A file of 2 GiB or more has a wrapped size in its row; the budget goes
// by its blob instead. Anything smaller reserves what the row says.
func TestBudgetSizeSurvivesTheInt32Size(t *testing.T) {
	galleryTestEnv(t)
	big, small := strings.Repeat("7", 64), strings.Repeat("8", 64)
	content := int64(3) << 30
	if err := os.WriteFile(blobPath(big), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(big))
	if err := os.Truncate(blobPath(big), content); err != nil { // sparse
		t.Fatal(err)
	}
	if got := budgetSize(&pb.File{Hash: big, Size: int32(content)}); got != content {
		t.Errorf("a 3 GiB file reserves %d bytes, want %d", got, content)
	}
	if err := os.WriteFile(blobPath(small), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(blobPath(small))
	if got := budgetSize(&pb.File{Hash: small, Size: 50}); got != 50 {
		t.Errorf("a small file reserves %d, want the row's 50", got)
	}
	if got := budgetSize(&pb.File{Hash: strings.Repeat("9", 64), Size: 70}); got != 70 {
		t.Errorf("a missing blob reserves %d, want the row's 70", got)
	}
}
