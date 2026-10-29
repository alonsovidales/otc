// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/go-sql-driver/mysql"
)

// The switch is read from the database once: on with its positions not
// all read is reading; a database before the release is off for good; an
// error reading it is off for now, and read again.
func TestPlacesSwitchIsReadOnce(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("select `places_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(1))
	mock.ExpectQuery("select `locations_backfilled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"d"}).AddRow(0))
	mg := &Manager{dao: dao.NewWithDB(db)}
	if !mg.PlacesEnabled() || !mg.PlacesEnabled() || !mg.placesReading() {
		t.Error("on, not all read: want on and reading")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	db2, mock2, _ := sqlmock.New()
	defer db2.Close()
	mock2.ExpectQuery("select `places_enabled` from `settings`").WillReturnError(&mysql.MySQLError{Number: 1054, Message: "Unknown column 'places_enabled'"})
	mg = &Manager{dao: dao.NewWithDB(db2)}
	if mg.PlacesEnabled() || mg.PlacesEnabled() || mg.placesReading() {
		t.Error("before the release: want off")
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	db3, mock3, _ := sqlmock.New()
	defer db3.Close()
	mock3.ExpectQuery("select `places_enabled` from `settings`").WillReturnError(errors.New("connection refused"))
	mock3.ExpectQuery("select `places_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(0))
	mg = &Manager{dao: dao.NewWithDB(db3)}
	if mg.PlacesEnabled() || mg.PlacesEnabled() {
		t.Error("an error, then off: want off")
	}
	if err := mock3.ExpectationsWereMet(); err != nil {
		t.Error(err, "(the error isn't read again)")
	}
}

// Off: processing keeps no position, the map answers ErrPlacesOff and the
// backfill reads nothing.
func TestPlacesOffKeepsNothing(t *testing.T) {
	_, ses := galleryTestEnv(t)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("select `places_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(0))
	mg := &Manager{dao: dao.NewWithDB(db)}
	gen := mg.places.gen.Load()
	mg.storeLocation(fmt.Sprintf("%064x", 0x9300), &exifinfo.Info{HasGPS: true, Latitude: 43.5, Longitude: 11.9})
	if mg.places.gen.Load() != gen {
		t.Error("the map told of a position not written")
	}
	if _, err := mg.PhotoPlaces(3, nil, nil, nil, "", false); !errors.Is(err, ErrPlacesOff) {
		t.Errorf("the map while off: %v", err)
	}
	mg.BackfillLocations(ses)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Turning it off deletes every position and the backfill's record in one
// transaction, forgets the map's index (one built from before is never
// put back) and ends reading; a backfill of the turn before writes
// nothing after it.
func TestPlacesTurnedOffDeletes(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := placesOn(&Manager{dao: dao.NewWithDB(db)})
	mg.placesSwitch.reading.Store(true)
	mg.places.set(&placesIndex{gen: mg.places.gen.Load(), built: time.Now()})
	before := mg.places.gen.Load()
	turn := mg.placesSwitch.turn.Load()

	mock.ExpectBegin()
	mock.ExpectExec("update `settings` set `places_enabled` = 0, `locations_backfilled` = 0").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `photo_locations`").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	if err := mg.SetPlacesEnabled(nil, false); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if mg.PlacesEnabled() || mg.placesReading() {
		t.Error("still on, or reading, after turning it off")
	}
	mg.places.mu.Lock()
	idx := mg.places.idx
	mg.places.mu.Unlock()
	if idx != nil {
		t.Error("the map's index kept")
	}
	mg.places.set(&placesIndex{gen: before, built: time.Now()})
	mg.places.mu.Lock()
	idx = mg.places.idx
	mg.places.mu.Unlock()
	if idx != nil {
		t.Error("an index read before the turn put back")
	}
	if wrote, _ := mg.placesWrite(&turn, func() error { t.Error("a write of the turn before ran"); return nil }); wrote {
		t.Error("placesWrite ran for the turn before")
	}
}

// A failed turn off changes nothing: Places stays on.
func TestPlacesTurnOffThatFailsStaysOn(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := placesOn(&Manager{dao: dao.NewWithDB(db)})
	mock.ExpectBegin()
	mock.ExpectExec("update `settings` set `places_enabled` = 0").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `photo_locations`").WillReturnError(errors.New("disk full"))
	mock.ExpectRollback()
	if err := mg.SetPlacesEnabled(nil, false); err == nil {
		t.Fatal("want the error")
	}
	if !mg.PlacesEnabled() {
		t.Error("off after a turn off that failed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A write under way when Places is turned off ends before the delete runs
// (so the delete takes it too), and none runs after it.
func TestPlacesTurnOffWaitsForAWrite(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := placesOn(&Manager{dao: dao.NewWithDB(db)})
	hash := fmt.Sprintf("%064x", 0x9400)
	// In this order, or sqlmock fails the call that comes early.
	mock.ExpectExec("insert into `photo_locations`").WillDelayFor(300 * time.Millisecond).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectBegin()
	mock.ExpectExec("update `settings` set `places_enabled` = 0, `locations_backfilled` = 0").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `photo_locations`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	wrote := make(chan struct{})
	go func() {
		mg.storeLocation(hash, &exifinfo.Info{HasGPS: true, Latitude: 43.5, Longitude: 11.9})
		close(wrote)
	}()
	time.Sleep(100 * time.Millisecond) // the write is under way
	if err := mg.SetPlacesEnabled(nil, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wrote:
	default:
		t.Error("turned off while the write was still under way")
	}
	// Off now: nothing more is written.
	mg.storeLocation(hash, &exifinfo.Info{HasGPS: true, Latitude: 1, Longitude: 2})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Turning it on starts reading at once, in the background - here finding
// the positions all read already, which ends reading.
func TestPlacesTurnedOnStartsTheBackfill(t *testing.T) {
	_, ses := galleryTestEnv(t)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("select `places_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(0))
	mock.ExpectExec("update `settings` set `places_enabled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `locations_backfilled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"d"}).AddRow(1))
	mg := &Manager{dao: dao.NewWithDB(db)}
	// Held, so the backfill waits until an index read while it reads is
	// in place: gone once the reading ends.
	mg.placesSwitch.backfillMu.Lock()
	if err := mg.SetPlacesEnabled(ses, true); err != nil {
		t.Fatal(err)
	}
	if !mg.PlacesEnabled() || !mg.placesReading() {
		t.Error("off, or not reading, after turning it on")
	}
	mg.places.set(&placesIndex{gen: mg.places.gen.Load(), built: time.Now()})
	mg.placesSwitch.backfillMu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for mg.placesReading() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if mg.placesReading() {
		t.Error("still reading: the backfill didn't run")
	}
	mg.places.mu.Lock()
	idx := mg.places.idx
	mg.places.mu.Unlock()
	if idx != nil {
		t.Error("the index read while reading outlived the reading")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
