// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
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

	mock.ExpectQuery("select `created` from `shared_links` where `uuid` = \\?").WithArgs("real").
		WillReturnRows(sqlmock.NewRows([]string{"created"}).AddRow(time.Now()))
	mock.ExpectQuery("select `created` from `shared_links` where `uuid` = \\?").WithArgs("made-up").
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
	// Malformed ids or secrets never reach the database.
	if mg.SharedGalleryKnown("not-a-uuid", secret) || mg.SharedGalleryKnown(id, "short") {
		t.Fatal("a malformed gallery link must not be known")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}
