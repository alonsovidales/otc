// SPDX-License-Identifier: AGPL-3.0-or-later

package mediastream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var (
	// ErrUnknownToken covers expired, never-issued, and tampered tokens
	// alike - deliberately one error, so nothing about which it was
	// leaks back to whoever sent it.
	ErrUnknownToken = errors.New("unknown or expired media token")
)

const (
	// MaxRangeBytes caps what one range request returns, whatever the
	// client asked for. A player asking for "bytes=0-" wants the whole
	// file; answering with the whole file is exactly the download this
	// issue exists to stop. Returning less than was asked for is
	// ordinary HTTP - the player comes back for the next span when it
	// needs it - and over the bridge it's what keeps one HTTP request to
	// one relay round trip.
	MaxRangeBytes int64 = 4 << 20 // 4 MiB

	// MinStreamableSize is the answer to "is streaming actually faster?"
	// Below it, it isn't: the file arrives in a single socket round trip
	// today, where streaming first spends a round trip minting a token
	// and then goes back for the bytes. So small clips keep the old
	// path, and this is the threshold that decides.
	MinStreamableSize int64 = 8 << 20 // 8 MiB

	// maxCachedPlaintext bounds what decrypting for playback can cost in
	// memory. Note the comparison point: today's non-streaming path
	// already holds the ciphertext, the plaintext AND a protobuf frame
	// of the same file in memory at once, so one cached plaintext is
	// strictly less than what playing the same video costs now.
	maxCachedPlaintext int64 = 512 << 20 // 512 MiB

	// plaintextTTL is how long a decrypted file stays cached after its
	// last use. Long enough that seeking around a video doesn't decrypt
	// it again each time, short enough that a finished video doesn't sit
	// in memory.
	plaintextTTL = 2 * time.Minute
)

// Stream is one readable view of a media resource, at a known size.
type Stream struct {
	Size int64
	Mime string
	rs   io.ReadSeeker
	// closer is non-nil only when the bytes are being read straight off
	// disk (publication media), which is the case that holds an fd.
	closer io.Closer
}

func (s *Stream) ReadSeeker() io.ReadSeeker { return s.rs }

func (s *Stream) Close() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}

// Server turns tokens into bytes. storagePath holds the owner's
// encrypted library; unencPath holds publication media, which is stored
// unencrypted (that's what lets it be range-read off disk without
// loading any of it).
type Server struct {
	store       *Store
	storagePath string
	unencPath   string

	mutex sync.Mutex
	cache map[string]*cachedPlaintext
}

type cachedPlaintext struct {
	content  []byte
	lastUsed time.Time
}

func NewServer(store *Store, storagePath, unencPath string) *Server {
	return &Server{
		store:       store,
		storagePath: storagePath,
		unencPath:   unencPath,
		cache:       make(map[string]*cachedPlaintext),
	}
}

func (sv *Server) Store() *Store { return sv.store }

// Open resolves a token and returns a stream over the resource it names.
// The caller must Close the stream.
func (sv *Server) Open(token string) (*Stream, error) {
	res, ok := sv.store.Resolve(token)
	if !ok {
		return nil, ErrUnknownToken
	}

	if res.Kind == KindPublicationMedia {
		// Already plaintext on disk, so the file itself is the
		// ReadSeeker - a seek to the middle of a 200MB video reads only
		// what's asked for, and nothing is held in memory.
		f, err := os.Open(fmt.Sprintf("%s/%s", sv.unencPath, res.Hash))
		if err != nil {
			return nil, fmt.Errorf("opening publication media: %w", err)
		}
		size := res.Size
		if size <= 0 {
			if info, statErr := f.Stat(); statErr == nil {
				size = info.Size()
			}
		}
		return &Stream{Size: size, Mime: res.Mime, rs: f, closer: f}, nil
	}

	content, err := sv.plaintext(res)
	if err != nil {
		return nil, err
	}
	return &Stream{Size: int64(len(content)), Mime: res.Mime, rs: bytes.NewReader(content)}, nil
}

// Range reads up to length bytes from offset, clamped to MaxRangeBytes
// and to the end of the file. It returns what it read plus the total
// size, which is what a client needs to build a Content-Range header.
func (sv *Server) Range(token string, offset, length int64) (content []byte, total int64, mime string, err error) {
	stream, err := sv.Open(token)
	if err != nil {
		return nil, 0, "", err
	}
	defer stream.Close()

	if offset < 0 || offset > stream.Size {
		return nil, stream.Size, stream.Mime, fmt.Errorf("offset %d outside the file", offset)
	}
	if length <= 0 || length > MaxRangeBytes {
		length = MaxRangeBytes
	}
	if remaining := stream.Size - offset; length > remaining {
		length = remaining
	}

	if _, err := stream.ReadSeeker().Seek(offset, io.SeekStart); err != nil {
		return nil, stream.Size, stream.Mime, fmt.Errorf("seeking to %d: %w", offset, err)
	}
	buf := make([]byte, length)
	// ReadFull, not Read: a single Read on a file is free to return
	// fewer bytes than asked for, which would silently truncate the span
	// the client believes it received.
	n, err := io.ReadFull(stream.ReadSeeker(), buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, stream.Size, stream.Mime, fmt.Errorf("reading %d bytes at %d: %w", length, offset, err)
	}

	return buf[:n], stream.Size, stream.Mime, nil
}

// plaintext returns the decrypted bytes of a library file, decrypting it
// at most once per plaintextTTL rather than on every range request - a
// player seeking around a video would otherwise re-decrypt the whole
// thing for each jump.
func (sv *Server) plaintext(res Resource) ([]byte, error) {
	sv.mutex.Lock()
	if c, ok := sv.cache[res.Hash]; ok {
		c.lastUsed = time.Now()
		content := c.content
		sv.mutex.Unlock()
		return content, nil
	}
	sv.mutex.Unlock()

	encContent, err := os.ReadFile(fmt.Sprintf("%s/%s", sv.storagePath, res.Hash))
	if err != nil {
		return nil, fmt.Errorf("reading stored file: %w", err)
	}
	if res.Decrypt == nil {
		return nil, errors.New("no way to decrypt this file")
	}
	content, err := res.Decrypt(encContent)
	if err != nil {
		return nil, fmt.Errorf("decrypting stored file: %w", err)
	}

	sv.mutex.Lock()
	defer sv.mutex.Unlock()
	sv.evictLocked()
	if int64(len(content)) <= maxCachedPlaintext {
		sv.cache[res.Hash] = &cachedPlaintext{content: content, lastUsed: time.Now()}
	}

	return content, nil
}

// evictLocked drops anything untouched for plaintextTTL, then keeps
// dropping the least recently used until what's left fits in
// maxCachedPlaintext. Decrypted bytes are held in memory on purpose -
// writing them to a temp file would put the owner's photos and videos on
// disk unencrypted, which is the one thing this project's storage design
// exists to avoid.
func (sv *Server) evictLocked() {
	now := time.Now()
	var total int64
	for hash, c := range sv.cache {
		if now.Sub(c.lastUsed) > plaintextTTL {
			delete(sv.cache, hash)
			continue
		}
		total += int64(len(c.content))
	}

	for total > maxCachedPlaintext {
		var oldestHash string
		var oldest time.Time
		for hash, c := range sv.cache {
			if oldestHash == "" || c.lastUsed.Before(oldest) {
				oldestHash, oldest = hash, c.lastUsed
			}
		}
		if oldestHash == "" {
			break
		}
		total -= int64(len(sv.cache[oldestHash].content))
		delete(sv.cache, oldestHash)
	}
}
