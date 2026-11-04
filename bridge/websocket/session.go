// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
)

// A video streamed through the bridge is fetched from its device a span at
// a time (the bridge's /media), and each span used to be a one-off request
// (ForwardOneOff) on a pool connection of its own, closed after it. A pool
// connection has sat idle, so every span started with a fresh TCP window
// (slow start after idle) and the device dialled a replacement for each,
// and the next span was asked for only once the last had gone out to the
// player: a 4K video, more than a home line's worth of spans a second,
// stalled behind its own round trips.
//
// A Session keeps one connection for all the requests of one HTTP response
// - its window stays open - and lets several be in flight on it at once,
// so the next span is on its way while one is written. The device limits
// stay what they were:
//   - each request in flight holds one of the device's cOneOffConcurrent
//     one-off slots, taken in turn with the static assets', so a video
//     never keeps a page from loading;
//   - a device lends at most cMaxSessionConns connections to sessions at
//     once; a session that finds none free sends its requests the one-off
//     way, as before;
//   - a session idle for cSessionIdle (a paused player) gives its
//     connection back, and takes another when it is asked again;
//   - a request is waited for at most oneOffMaxWait, however long the
//     device says it works on it, and Close (the client hung up) ends the
//     ones in flight at once, so a slot is never held for nobody.

// cMaxSessionConns caps the connections a device keeps for sessions; the
// rest of its pool stays for the clients and the one-offs. Var so tests
// can shrink it.
var cMaxSessionConns = 4

// cSessionIdle is how long a session with nothing in flight keeps its
// connection. Var so tests can shrink it.
var cSessionIdle = 10 * time.Second

var (
	errSessionClosed = errors.New("session closed")                                       // i18n-ignore: logged, never sent
	errNoConnection  = errors.New("no available connections in the pool for this device") // i18n-ignore: logged, never sent
)

// Session sends the requests of one HTTP response to domain's device.
// Forward may be called by several goroutines at once; their envelopes
// must carry distinct ids. Close when the response is done.
type Session struct {
	mg     *Manager
	domain string
	pool   *bridgePool // nil: the device has no connection here

	mu       sync.Mutex
	cond     *sync.Cond
	relay    *deviceRelay // the connection kept, nil when none
	holding  bool         // holds one of the domain's session places
	taking   bool         // a Forward is taking a connection for the session
	inFlight int
	idle     *time.Timer
	closed   bool
	// loose are the connections taken for one request alone (the
	// device's session places all taken), closed with the session.
	loose map[*deviceRelay]struct{}
	// done is closed by Close: a request waiting for a slot stops.
	done chan struct{}
}

// OpenSession starts a session with domain's device. It costs nothing
// until its first Forward.
func (mg *Manager) OpenSession(domain string) *Session {
	mg.bridgesMu.RLock()
	pool := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	s := &Session{mg: mg, domain: domain, pool: pool, done: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// sessionPlaces is domain's places for sessions' connections.
func (mg *Manager) sessionPlaces(domain string) chan struct{} {
	ch, _ := mg.sessionConns.LoadOrStore(domain, make(chan struct{}, cMaxSessionConns))
	return ch.(chan struct{})
}

// Forward sends frame to the device and returns its reply, as
// ForwardOneOff does: within the device's one-off slots, each attempt
// given cOneOffForwardTimeout and all of them oneOffMaxWait, at most
// cOneOffMaxAttempts connections spent - the session's own after it
// failed, a new one taken in its place. Close ends it at once.
func (s *Session) Forward(frame []byte) ([]byte, error) {
	if s.pool == nil {
		return nil, errors.New("device is offline") // i18n-ignore: logged, never sent
	}
	slot := s.mg.oneOffSlot(s.domain)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-time.After(cOneOffSlotWait):
		return nil, ErrDeviceBusy
	case <-s.done:
		return nil, errSessionClosed
	}
	s.begin()
	defer s.end()

	giveUp := time.Now().Add(oneOffMaxWait())
	for attempt := 0; attempt < cOneOffMaxAttempts && time.Now().Before(giveUp); attempt++ {
		relay, kept, err := s.connection(min(cOneOffConnWait, time.Until(giveUp)))
		if err != nil {
			return nil, err
		}
		resp, release, err := relay.forwardWithTimeout(frame, cOneOffForwardTimeout, time.Until(giveUp))
		if !kept {
			s.letGo(relay)
		}
		if err != nil {
			if kept {
				s.drop(relay)
			}
			if s.isClosed() {
				return nil, errSessionClosed // its client is gone: nothing to log
			}
			log.Error("error forwarding a session's request, trying another connection:", err)
			continue
		}
		// As ForwardOneOff's: budgeted while read, the reply is the
		// caller's from here.
		release()
		if s.mg.dao != nil {
			if dbErr := s.mg.dao.RecordDeviceActivity(s.domain, int64(len(frame)), int64(len(resp))); dbErr != nil {
				log.Error("error recording device activity:", dbErr)
			}
		}
		return resp, nil
	}
	return nil, fmt.Errorf("device did not answer after %d attempts", cOneOffMaxAttempts) // i18n-ignore: logged, never sent
}

// connection is the connection for the next request: the session's own,
// one taken for it, or - with the device's session places all taken - one
// for this request alone (kept false: the caller lets it go). It waits up
// to wait for the device to have one.
func (s *Session) connection(wait time.Duration) (relay *deviceRelay, kept bool, err error) {
	s.mu.Lock()
	for s.taking && !s.closed {
		s.cond.Wait()
	}
	if s.closed {
		s.mu.Unlock()
		return nil, false, errSessionClosed
	}
	if s.relay != nil {
		relay = s.relay
		s.mu.Unlock()
		return relay, true, nil
	}
	if !s.holding {
		select {
		case s.mg.sessionPlaces(s.domain) <- struct{}{}:
			s.holding = true
		default:
		}
	}
	if !s.holding {
		s.mu.Unlock()
		if relay = takeAvailable(s.pool, wait); relay == nil {
			return nil, false, errNoConnection
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			relay.Close()
			return nil, false, errSessionClosed
		}
		if s.loose == nil {
			s.loose = map[*deviceRelay]struct{}{}
		}
		s.loose[relay] = struct{}{}
		s.mu.Unlock()
		return relay, false, nil
	}
	s.taking = true
	s.mu.Unlock()
	relay = takeAvailable(s.pool, wait)
	s.mu.Lock()
	s.taking = false
	s.cond.Broadcast()
	switch {
	case relay == nil:
		s.giveBackLocked()
		s.mu.Unlock()
		return nil, false, errNoConnection
	case s.closed:
		s.mu.Unlock()
		relay.Close()
		return nil, false, errSessionClosed
	}
	s.relay = relay
	s.mu.Unlock()
	return relay, true, nil
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// letGo closes a connection taken for one request alone, once done.
func (s *Session) letGo(relay *deviceRelay) {
	s.mu.Lock()
	delete(s.loose, relay)
	s.mu.Unlock()
	relay.Close()
}

// drop closes the session's connection after it failed; the next request
// takes another in its place.
func (s *Session) drop(relay *deviceRelay) {
	s.mu.Lock()
	if s.relay == relay {
		s.relay = nil
	}
	s.mu.Unlock()
	relay.Close()
}

// begin and end bracket a request in flight; once none is, the session's
// connection is kept for cSessionIdle more.
func (s *Session) begin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight++
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
}

func (s *Session) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight--; s.inFlight == 0 && !s.closed && s.relay != nil {
		s.idle = time.AfterFunc(cSessionIdle, s.idleOut)
	}
}

// idleOut gives the connection of a session nothing was asked of back:
// the device's places are for sessions in use (a paused player's comes
// back for a new one when it plays on).
func (s *Session) idleOut() {
	s.mu.Lock()
	if s.inFlight > 0 || s.closed || s.taking {
		s.mu.Unlock()
		return
	}
	relay := s.relay
	s.relay = nil
	s.giveBackLocked()
	s.mu.Unlock()
	if relay != nil {
		relay.Close()
	}
}

// giveBackLocked gives the session's place back; under mu.
func (s *Session) giveBackLocked() {
	if s.holding {
		s.holding = false
		<-s.mg.sessionPlaces(s.domain)
	}
}

// Close ends the session: its connections are closed (a device's
// connection serves one client, and the session was its client), and its
// requests still in flight or waiting for a slot fail at once. Safe to
// call while a Forward runs - the HTTP side calls it when its client hangs
// up, so a request the device is slow on gives its slot back then.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	relays := make([]*deviceRelay, 0, 1+len(s.loose))
	if s.relay != nil {
		relays = append(relays, s.relay)
	}
	for r := range s.loose {
		relays = append(relays, r)
	}
	s.relay, s.loose = nil, nil
	s.giveBackLocked()
	s.cond.Broadcast()
	s.mu.Unlock()
	for _, r := range relays {
		r.Close()
	}
}
