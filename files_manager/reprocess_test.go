// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
)

// Reprocess's fresh-start/resume paths need a live *session.Session (for
// ses.Decrypt inside the worker goroutine) - constructing one from outside
// the session package needs a real dao.New round trip, same reasoning as
// processFaces itself staying untested here (see face_processing_test.go's
// own doc comment) and DetectFaces needing real models. This test only
// exercises the one branch that's reachable with neither: Reprocess must
// not touch the database at all - not even to read the current state - if
// a run started by this same process is already active, or two calls in
// quick succession (e.g. a doubled button tap) would both spawn their own
// worker goroutine racing over the same rows.
func TestReprocessNoOpWhenAlreadyRunning(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mg := &Manager{dao: dao.NewWithDB(db)}
	mg.reprocessing = true

	if err := mg.Reprocess(nil, false); err != nil {
		t.Errorf("Reprocess() while already running = %v, want nil (no-op)", err)
	}
	if !mg.reprocessing {
		t.Error("Reprocess()'s no-op path must not clear the in-progress flag - that's the running goroutine's own job")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("Reprocess() while already running made a database call, want none: %v", err)
	}
}

// CancelReprocess is the "stop" button's only job - these two cover the
// no-op case (nothing running - must not panic on a nil cancel func) and
// the real case (an active run's context actually gets cancelled).
func TestCancelReprocessNoOpWhenNothingRunning(t *testing.T) {
	mg := &Manager{}
	mg.CancelReprocess() // must not panic
}

func TestCancelReprocessCancelsTheActiveRun(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	called := false
	mg := &Manager{}
	mg.reprocessCancel = func() {
		called = true
		cancel()
	}

	mg.CancelReprocess()

	if !called {
		t.Error("CancelReprocess() didn't invoke the stored cancel func")
	}
}
