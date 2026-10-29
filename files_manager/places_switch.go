// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
)

// The Places switch (settings.places_enabled), off unless the user turns
// it on: the positions are kept unencrypted, so the device keeps them only
// while it is on. Off, nothing writes a photo_locations row, the backfill
// doesn't run and GetPhotoPlaces answers ErrPlacesOff; turning it off
// deletes every row before it answers, and turning it on reads them again
// in the background.
//
// mu is held for reading by every write of a position, which checks the
// switch under it (placesWrite), and for writing while the switch turns,
// so turning it off waits for a write under way and no write lands after
// the delete. turn counts the turns: a backfill started before one stops
// at its next photo, and its writes don't run.
type placesSwitch struct {
	mu     sync.RWMutex
	loadMu sync.Mutex
	loaded atomic.Bool
	on     atomic.Bool
	turn   atomic.Uint64
	// backfillMu: one backfill at a time.
	backfillMu sync.Mutex
	// reading is PhotoPlaces.reading: on, and the positions of what the
	// device held aren't all read yet, with no backfill of this turn
	// ended.
	reading atomic.Bool
}

// ErrPlacesOff is a map asked for while Places is off (GetPhotoPlaces'
// error_code "places_off").
var ErrPlacesOff = errors.New("places is off") // i18n-ignore: the reply's code and text are the websocket layer's

// loadPlaces reads the switch from the database once. A database before
// the release that added it reads as off; an error reading it is off for
// now and read again next time.
func (mg *Manager) loadPlaces() {
	if mg == nil || mg.dao == nil {
		return
	}
	s := &mg.placesSwitch
	if s.loaded.Load() {
		return
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if s.loaded.Load() {
		return
	}
	on, err := mg.dao.PlacesEnabled()
	if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
		log.Error("places: could not read whether Places is on, off for now:", err)
		return
	}
	reading := false
	if on {
		done, err := mg.dao.LocationsBackfilled()
		reading = err == nil && !done
	}
	s.mu.Lock()
	if !s.loaded.Load() {
		s.on.Store(on)
		s.reading.Store(reading)
		s.loaded.Store(true)
	}
	s.mu.Unlock()
}

// PlacesEnabled is whether Places is on for this library.
func (mg *Manager) PlacesEnabled() bool {
	if mg == nil {
		return false
	}
	mg.loadPlaces()
	return mg.placesSwitch.on.Load()
}

// SetPlacesEnabled turns Places on or off for this library. Off deletes
// every position kept, and the record that the backfill ran, before it
// returns, and drops the map's index. On reads the positions of what the
// device holds in the background, with ses's key.
func (mg *Manager) SetPlacesEnabled(ses *session.Session, on bool) error {
	mg.loadPlaces()
	s := &mg.placesSwitch
	s.mu.Lock()
	if err := mg.dao.SetPlacesEnabled(on); err != nil {
		s.mu.Unlock()
		return err
	}
	s.on.Store(on)
	s.loaded.Store(true)
	s.turn.Add(1)
	// On: what the device holds is read again from the start (a turn on
	// while on finds the backfill done, if it is, and ends it at once).
	s.reading.Store(on)
	s.mu.Unlock()
	mg.places.reset()
	if on {
		log.Info("places: on, reading where the photos were taken")
		go mg.BackfillLocations(ses)
	} else {
		log.Info("places: off, every position deleted")
	}
	return nil
}

// placesWrite runs write while Places is on, under the switch's read lock,
// and is whether it ran. turn, when set, is the turn a backfill started
// in: a write of an older turn doesn't run.
func (mg *Manager) placesWrite(turn *uint64, write func() error) (bool, error) {
	mg.loadPlaces()
	s := &mg.placesSwitch
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.on.Load() || (turn != nil && s.turn.Load() != *turn) {
		return false, nil
	}
	return true, write()
}

// placesReading is PhotoPlaces.reading.
func (mg *Manager) placesReading() bool {
	return mg.placesSwitch.reading.Load()
}
