// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"sync"
	"time"

	"github.com/alonsovidales/otc/bridge/clientaddr"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
)

// A device lends the bridge a few dozen connections, and a client paired
// with one keeps it for as long as its socket stays open. Device names are
// public, so anyone can ask for one: before this, one address could pair
// as many sockets as the device allowed, send nothing more, and leave the
// owner, the family and the friends' devices outside ("device
// unreachable") for as long as it liked.
//
// A session that has signed in - the device accepted a password, a
// session token or a friend's handshake - is the device's own, and keeps
// the limits it always had. One that hasn't (a stranger, a sign-in page
// before the password, a visitor of a shared gallery):
//   - counts against a share of the device for its address (an IPv4
//     address, an IPv6 /64 and its /48): maxAnonPerAddr, a quarter of the
//     device's cap, so one address can never fill it;
//   - is closed once it has sent nothing, with nothing in flight, for
//     cAnonPairedIdle (the web app and the shared pages reconnect with
//     their next request);
//   - at the device's cap, makes room for a newcomer: of the address
//     holding the most of them, the one paired the longest ago is closed
//     - from cEvictAfter after its pairing with nothing in flight, from
//     cEvictBusyAfter whatever it is doing. By age, not by idleness: a
//     stranger sending a small request every few seconds on each of its
//     sessions was never idle, and kept the device's people out.
//
// Signing in is read off the relayed traffic, so this works with devices
// of every release: a request whose payload is an Auth, an AuthWithToken
// or an AuthAsFriend, answered with an Ack whose ok is set.

var (
	// cAnonPairedIdle: a session that hasn't signed in and has nothing in
	// flight is closed after this long without a message.
	cAnonPairedIdle = 2 * time.Minute
	// cAnonShareWait: how long a newcomer waits for room in its
	// address's share - a household restoring many tabs at once signs
	// them in within a moment.
	cAnonShareWait = 3 * time.Second
	// cEvictAfter: a session must have been paired this long before a
	// newcomer may close it, so one about to sign in is left alone.
	cEvictAfter = 5 * time.Second
	// cEvictBusyAfter: paired this long, a session that never signed in
	// may be closed for a newcomer with a request in flight too (answered
	// "unreachable", as when a device connection dies).
	cEvictBusyAfter = 60 * time.Second
)

// maxAnonPerAddr is how many sessions that haven't signed in one address
// may hold with one device: [bridge] max-unauthenticated-per-addr, a
// quarter of maxPairedPerDevice by default.
func maxAnonPerAddr() int {
	return bridgeLimit("max-unauthenticated-per-addr", max(1, maxPairedPerDevice()/4))
}

// anonKeys are the shares a session from addr with host counts against
// until it signs in: the address's bucket, and an IPv6 address's /48.
func anonKeys(addr, host string) []string {
	keys := []string{clientaddr.Bucket(addr) + "|" + host}
	if w := clientaddr.Wide(addr); w != "" {
		keys = append(keys, w+"|"+host)
	}
	return keys
}

// reserveAnon counts one more session that hasn't signed in against each
// of keys, unless one of them already holds maxAnonPerAddr.
func (mg *Manager) reserveAnon(keys []string) bool {
	limit := maxAnonPerAddr()
	mg.pairedMu.Lock()
	defer mg.pairedMu.Unlock()
	for _, k := range keys {
		if mg.anonByKey[k] >= limit {
			return false
		}
	}
	if mg.anonByKey == nil {
		mg.anonByKey = map[string]int{}
	}
	for _, k := range keys {
		mg.anonByKey[k]++
	}
	return true
}

// waitAnon is reserveAnon, retried for up to wait.
func (mg *Manager) waitAnon(keys []string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if mg.reserveAnon(keys) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (mg *Manager) releaseAnon(keys []string) {
	mg.pairedMu.Lock()
	defer mg.pairedMu.Unlock()
	for _, k := range keys {
		if mg.anonByKey[k] <= 1 {
			delete(mg.anonByKey, k)
		} else {
			mg.anonByKey[k]--
		}
	}
}

// clientSession is a client socket paired with one of a device's relays,
// from its pairing to its end.
type clientSession struct {
	host string
	keys []string // anonKeys, released when it signs in or ends
	conn *gorilla.Conn
	// relay is closed when a newcomer evicts the session.
	relay *deviceRelay

	// Lock order: Manager.pairedMu before mu, never the other way.
	mu       sync.Mutex
	anon     bool      // no sign-in seen on it yet
	inFlight int       // requests relayed and not answered yet
	lastUsed time.Time // its last request or answer
	pairedAt time.Time
	evicted  bool
	// ids counts the requests in flight by envelope id, and dup marks an
	// id sent again while one was in flight: an answer to it could be
	// either's, so it is never read as a sign-in.
	ids map[int32]int
	dup map[int32]bool
}

func newClientSession(host string, keys []string, conn *gorilla.Conn, relay *deviceRelay) *clientSession {
	now := time.Now()
	return &clientSession{host: host, keys: keys, conn: conn, relay: relay, anon: true, lastUsed: now, pairedAt: now}
}

// addSession lists s among host's sessions (evictAnon looks there).
func (mg *Manager) addSession(s *clientSession) {
	mg.pairedMu.Lock()
	defer mg.pairedMu.Unlock()
	if mg.sessions == nil {
		mg.sessions = map[string]map[*clientSession]struct{}{}
	}
	m := mg.sessions[s.host]
	if m == nil {
		m = map[*clientSession]struct{}{}
		mg.sessions[s.host] = m
	}
	m[s] = struct{}{}
}

// removeSession is the end of s: unlisted, and its address's share given
// back if it never signed in.
func (mg *Manager) removeSession(s *clientSession) {
	s.mu.Lock()
	anon := s.anon
	s.anon = false
	s.mu.Unlock()
	if anon {
		mg.releaseAnon(s.keys)
	}
	mg.pairedMu.Lock()
	defer mg.pairedMu.Unlock()
	if m := mg.sessions[s.host]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(mg.sessions, s.host)
		}
	}
}

// signedIn marks s signed in and gives its address's share back; false
// if it already was.
func (mg *Manager) signedIn(s *clientSession) bool {
	s.mu.Lock()
	was := s.anon
	s.anon = false
	if was {
		s.conn.SetReadDeadline(time.Time{})
	}
	s.mu.Unlock()
	if was {
		mg.releaseAnon(s.keys)
	}
	return was
}

// idleFrom starts s's idle clock at the moment of its pairing.
func (s *clientSession) idleFrom(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUsed = now
	if s.anon && s.inFlight == 0 {
		s.conn.SetReadDeadline(now.Add(cAnonPairedIdle))
	}
}

// started records request id on its way to the device: while one is in
// flight nothing times the session out.
func (s *clientSession) started(id int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight++
	s.lastUsed = time.Now()
	if s.ids == nil {
		s.ids = map[int32]int{}
	}
	s.ids[id]++
	if s.ids[id] > 1 {
		if s.dup == nil {
			s.dup = map[int32]bool{}
		}
		s.dup[id] = true
	}
	if s.anon {
		s.conn.SetReadDeadline(time.Time{})
	}
}

// finished records the end of request id (answered or not); accepted is
// whether it was a sign-in the device accepted. It reports whether that
// signed s in - the caller then gives the share back (Manager.signedIn's
// work, done here under one lock with the clock).
func (s *clientSession) finished(id int32, accepted bool) (signedIn bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dup := s.dup[id]
	if s.ids[id]--; s.ids[id] <= 0 {
		delete(s.ids, id)
		delete(s.dup, id)
	}
	s.inFlight--
	s.lastUsed = time.Now()
	switch {
	case s.anon && accepted && !dup:
		s.anon = false
		s.conn.SetReadDeadline(time.Time{})
		return true
	case s.anon && s.inFlight == 0:
		s.conn.SetReadDeadline(s.lastUsed.Add(cAnonPairedIdle))
	}
	return false
}

// anonymous reports whether s has not signed in (nil: not paired).
func (s *clientSession) anonymous() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.anon
}

// idle reports whether s has no request in flight.
func (s *clientSession) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight == 0
}

func (s *clientSession) wasEvicted() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evicted
}

// evictAnon closes one of host's sessions that hasn't signed in, to make
// room for a newcomer at the device's cap: one paired cEvictAfter ago or
// more with nothing in flight, or cEvictBusyAfter ago or more. Of the
// address holding the most of them (its bucket or, for IPv6, its /48,
// whichever holds more), one with nothing in flight first, then the one
// paired the longest ago. Its device connection is closed before this
// returns, so the cap counts it out; its socket is hung up on once its
// requests in flight are answered "unreachable" (deviceRelay.dead, and
// the relayed request's own error). Reports whether there was one.
func (mg *Manager) evictAnon(host string) bool {
	now := time.Now()
	var victim *clientSession
	var victimShare int
	var victimBusy bool
	var victimPaired time.Time
	mg.pairedMu.Lock()
	for s := range mg.sessions[host] {
		s.mu.Lock()
		age := now.Sub(s.pairedAt)
		busy := s.inFlight > 0
		ok := s.anon && !s.evicted && age >= cEvictAfter && (!busy || age >= cEvictBusyAfter)
		paired := s.pairedAt
		s.mu.Unlock()
		if !ok {
			continue
		}
		share := 0
		for _, k := range s.keys {
			share = max(share, mg.anonByKey[k])
		}
		if victim == nil || share > victimShare ||
			(share == victimShare && (victimBusy && !busy || victimBusy == busy && paired.Before(victimPaired))) {
			victim, victimShare, victimBusy, victimPaired = s, share, busy, paired
		}
	}
	if victim != nil {
		victim.mu.Lock()
		victim.evicted = true
		victim.mu.Unlock()
	}
	mg.pairedMu.Unlock()
	if victim == nil {
		return false
	}
	mg.logRefusal("evicted|"+host, "closed a session that never signed in, for a new client of", host) // i18n-ignore: a log line
	if victim.relay == nil {
		victim.conn.Close()
		return true
	}
	victim.relay.Close()
	return true
}

// Field numbers sign-ins are read by (proto/messages.proto).
const (
	cEnvIDField     = 1  // ReqEnvelope.id, RespEnvelope.id
	cReqLangField   = 2  // ReqEnvelope.lang: with id, all that isn't the payload
	cReqAuthField   = 12 // ReqEnvelope.req_auth
	cReqFriendField = 40 // ReqEnvelope.req_auth_as_friend
	cReqTokenField  = 84 // ReqEnvelope.req_auth_with_token
	cRespErrorField = 2  // RespEnvelope.error
	cRespLastPlain  = 4  // RespEnvelope.error_code: fields past it are the payload
	cRespAckField   = 11 // RespEnvelope.resp_ack
	cAckOkField     = 1  // Ack.ok
)

// isSignIn reports whether frame is a sign-in request: its payload - the
// last field but id and lang, which is what the device decodes it as - is
// an Auth, an AuthWithToken or an AuthAsFriend. Read from the wire format,
// without decoding the rest.
func isSignIn(frame []byte) bool {
	var last protowire.Number
	var lastTyp protowire.Type
	for b := frame; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return false
		}
		b = b[m:]
		if num != cEnvIDField && num != cReqLangField {
			last, lastTyp = num, typ
		}
	}
	return lastTyp == protowire.BytesType && (last == cReqAuthField || last == cReqFriendField || last == cReqTokenField)
}

// isAccepted reports whether resp is how a device answers a sign-in it
// accepted: no error, and an Ack (the payload) whose ok is set.
func isAccepted(resp []byte) bool {
	failed, ok := false, false
	var last protowire.Number
	for b := resp; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		switch {
		case num == cRespErrorField && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return false
			}
			failed = v != 0
			b = b[m:]
			continue
		case num == cRespAckField && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return false
			}
			if set, present := ackOK(v); present {
				ok = set
			}
			last = num
			b = b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return false
		}
		b = b[m:]
		if num > cRespLastPlain {
			last = num
		}
	}
	return !failed && last == cRespAckField && ok
}

// ackOK is an encoded Ack's ok, and whether it carries one.
func ackOK(ack []byte) (ok, present bool) {
	for b := ack; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false, false
		}
		b = b[n:]
		if num == cAckOkField && typ == protowire.VarintType {
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return false, false
			}
			ok, present = v != 0, true
			b = b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return false, false
		}
		b = b[m:]
	}
	return ok, present
}
