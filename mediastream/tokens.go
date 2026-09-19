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
	// Decrypt turns the on-disk bytes into plaintext for
	// KindLibraryFile. Held as a function rather than the *session it
	// came from so this package doesn't depend on session at all, and so
	// a token can never be used to reach anything else that session can.
	Decrypt func([]byte) ([]byte, error)
}

type entry struct {
	res       Resource
	expiresAt time.Time
}

type Store struct {
	mutex  sync.Mutex
	tokens map[string]*entry
}

func NewStore() *Store {
	return &Store{tokens: make(map[string]*entry)}
}

// Issue mints a token for res and returns it with its expiry.
func (s *Store) Issue(res Resource) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expiresAt := time.Now().Add(TTL)

	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.purgeExpiredLocked()
	s.tokens[token] = &entry{res: res, expiresAt: expiresAt}

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
		delete(s.tokens, token)
		return Resource{}, false
	}

	return e.res, true
}

// purgeExpiredLocked keeps the map from growing without bound. Called on
// mint rather than from a timer: a device nobody is streaming from has
// nothing to clean up, and one that is gets swept on every new token.
func (s *Store) purgeExpiredLocked() {
	now := time.Now()
	for token, e := range s.tokens {
		if now.After(e.expiresAt) {
			delete(s.tokens, token)
		}
	}
}
