// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mediastream backs issue #110's video streaming: it hands out
// short-lived, opaque tokens that stand in for "this one file, for this
// one already-authenticated session", so a <video> element or AVPlayer
// can fetch it over ordinary HTTP with range requests.
//
// Why a token at all: every other way a client reads a file goes through
// the authenticated WebSocket, but a media player can't speak that
// protocol - it needs a URL. The token is what carries the authorization
// the socket already established across to the plain HTTP request, and it
// is deliberately the *only* credential that request has, which is why it
// is opaque, bound to a single resource, and expires. It is never a
// general "read any file" capability: resolving one yields exactly the
// resource it was minted for.
//
// Process-local and deliberately not persisted - like the sessions it
// depends on, tokens don't survive a restart (see session/tokens.go,
// which makes the same call for the same reason).
//
// reader.go is the other half: turning a resolved token into the actual
// bytes, over HTTP on the device itself or over the relay tunnel via the
// bridge.
package mediastream

import (
	"crypto/rand"
	"encoding/base64"
	"github.com/alonsovidales/otc/blobstore"
	"sync"
	"time"
)

// Kind says where the bytes come from, which decides whether they need
// decrypting at all.
type Kind int

const (
	// KindLibraryFile is one of the owner's own files, stored encrypted
	// (see files_manager) - the plaintext only ever exists in memory.
	KindLibraryFile Kind = iota
	// KindPublicationMedia is a post's media, which lives unencrypted in
	// unenc-storage-path and can therefore be range-read straight off
	// disk without loading any of it.
	KindPublicationMedia
)

// TTL is how long a minted token stays usable. Long enough to watch a
// long video through without the URL dying mid-playback, short enough
// that a URL which leaks (a browser history entry, a proxy log) stops
// being useful quickly.
const TTL = time.Hour

// ReuseWindow: IssueShared hands out the same token for the same resource
// for this long after minting it, so asking again and again (anyone with a
// gallery link can) doesn't grow the store.
const ReuseWindow = 5 * time.Minute

// cPurgeEvery: how often minting sweeps expired tokens out.
const cPurgeEvery = time.Minute

type Resource struct {
	Kind Kind
	// Path for KindLibraryFile.
	Path string
	// Hash addresses the bytes on disk for both kinds; PubUuid is only
	// meaningful for KindPublicationMedia.
	PubUuid string
	Hash    string
	Mime    string
	Size    int64
	// Keys open a library file (segmented encryption, blobstore) - the
	// session that asked for the token. Nil for publication media, which
	// is stored unencrypted.
	Keys blobstore.Keys
}

type entry struct {
	res       Resource
	expiresAt time.Time
	// reuseKey: what IssueShared minted it for, if anything.
	reuseKey string
}

type Store struct {
	mutex  sync.Mutex
	tokens map[string]*entry
	// byKey: the token IssueShared last minted for each reuse key.
	byKey     map[string]string
	lastPurge time.Time
}

func NewStore() *Store {
	return &Store{tokens: make(map[string]*entry), byKey: make(map[string]string)}
}

// Issue mints a token for res and returns it with its expiry.
func (s *Store) Issue(res Resource) (string, time.Time, error) {
	return s.IssueShared("", res)
}

// IssueShared is Issue, except that a token minted for the same key (one
// resource, e.g. a gallery item) less than ReuseWindow ago is returned
// again instead of a new one. The caller authorises every request before
// asking; the key must name exactly the resource, and only one anyone so
// authorised may share. An older token is never revoked - a player may
// still be using it - it just expires. An empty key never reuses.
func (s *Store) IssueShared(key string, res Resource) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	expiresAt := now.Add(TTL)

	s.mutex.Lock()
	defer s.mutex.Unlock()
	if key != "" {
		if old, ok := s.byKey[key]; ok {
			if e, ok := s.tokens[old]; ok && e.expiresAt.Sub(now) > TTL-ReuseWindow {
				return old, e.expiresAt, nil
			}
		}
	}
	// A full sweep at most once a minute, not on every mint: it holds the
	// lock every /media range request (and ffmpeg's reads) waits on.
	// Resolve drops an expired token it meets anyway.
	if now.Sub(s.lastPurge) > cPurgeEvery {
		s.purgeExpiredLocked()
		s.lastPurge = now
	}
	s.tokens[token] = &entry{res: res, expiresAt: expiresAt, reuseKey: key}
	if key != "" {
		s.byKey[key] = token
	}

	return token, expiresAt, nil
}

// Resolve returns the resource a token was minted for. Unlike a session
// token (session/tokens.go) this is NOT single-use: a player makes one
// request per range it needs, and every one of them carries the same
// token.
func (s *Store) Resolve(token string) (Resource, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	e, ok := s.tokens[token]
	if !ok {
		return Resource{}, false
	}
	if time.Now().After(e.expiresAt) {
		s.deleteLocked(token, e)
		return Resource{}, false
	}

	return e.res, true
}

// Revoke ends a token before its expiry - one minted for the device's own
// processing (ffmpeg reading a video over loopback), once it's done.
func (s *Store) Revoke(token string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if e, ok := s.tokens[token]; ok {
		s.deleteLocked(token, e)
	}
}

func (s *Store) deleteLocked(token string, e *entry) {
	delete(s.tokens, token)
	if e.reuseKey != "" && s.byKey[e.reuseKey] == token {
		delete(s.byKey, e.reuseKey)
	}
}

// purgeExpiredLocked keeps the map from growing without bound. Called on
// mint rather than from a timer: a device nobody is streaming from has
// nothing to clean up, and one that is gets swept as it mints tokens.
func (s *Store) purgeExpiredLocked() {
	now := time.Now()
	for token, e := range s.tokens {
		if now.After(e.expiresAt) {
			s.deleteLocked(token, e)
		}
	}
}
