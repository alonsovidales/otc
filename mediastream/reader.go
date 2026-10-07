// SPDX-License-Identifier: AGPL-3.0-or-later

package mediastream

import (
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/blobstore"
	"io"
	"mime"
	"os"
	"path"
	"strings"
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

)

// IsStreamable reports whether this kind of media is worth serving as a
// byte stream at all.
//
// Only video is. An image has to be complete before it can be shown, so
// there is nothing to gain - and, less obviously, the normal fetch path
// converts HEIC to JPEG on its way out (issue #44), which a raw byte
// range bypasses entirely: an iPhone photo streamed straight off disk
// reaches a client as something it can't display.
//
// Checked on the device rather than trusted to each client: a client
// asking for a stream of a photo is a bug, and answering "no" here is
// what keeps that bug from becoming a video player opening over
// someone's holiday snap (which is exactly what it did on iOS).
func IsStreamable(mime string) bool {
	return strings.HasPrefix(mime, "video/")
}

// Stream is one readable view of a media resource, at a known size.
type Stream struct {
	Size int64
	Mime string
	// Name is the file's own name, the last element of the resource's
	// path - what a download of it is saved as. Empty for a resource with
	// no path (a post's media is addressed by its hash alone).
	Name string
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
}

func NewServer(store *Store, storagePath, unencPath string) *Server {
	return &Server{
		store:       store,
		storagePath: storagePath,
		unencPath:   unencPath,
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
		return &Stream{Size: size, Mime: res.Mime, Name: baseName(res.Path), rs: f, closer: f}, nil
	}

	// A library file: opened in its segmented encryption, so a range
	// decrypts only the segments it covers - never the whole file (which
	// used to be decrypted, and cached, whole: gigabytes for a long video).
	if res.Keys == nil {
		return nil, errors.New("no way to decrypt this file")
	}
	blob, err := blobstore.Open(fmt.Sprintf("%s/%s", sv.storagePath, res.Hash), res.Keys)
	if err != nil {
		return nil, fmt.Errorf("opening stored file: %w", err)
	}
	return &Stream{Size: blob.Size(), Mime: res.Mime, Name: baseName(res.Path), rs: io.NewSectionReader(blob, 0, blob.Size()), closer: blob}, nil
}

// baseName is a path's last element, or "" when there is none.
func baseName(p string) string {
	if p == "" {
		return ""
	}
	name := path.Base(p)
	if name == "/" || name == "." {
		return ""
	}
	return name
}

// ServedType is the Content-Type to serve a resource stored with this mime
// under, and whether a browser may show it in place: video, audio and the
// raster image types, none of which can run script. Anything else -
// including a value that isn't exactly one well-formed type, which a
// browser may read differently from this check ("video/mp4,text/html"
// passes IsStreamable, and Chrome renders it as a page) - is
// application/octet-stream, bytes to save.
//
// Both ways a token's bytes leave the device go through it: /media here,
// and ReqGetMediaRange, whose type the bridge copies into its response on
// the device's own subdomain - the app's origin.
func ServedType(stored string) (ctype string, inline bool) {
	mt, _, err := mime.ParseMediaType(stored)
	if err != nil {
		return "application/octet-stream", false
	}
	switch {
	case strings.HasPrefix(mt, "video/"), strings.HasPrefix(mt, "audio/"):
		return stored, true
	}
	switch mt {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/avif", "image/bmp",
		"image/heic", "image/heif", "image/tiff":
		return stored, true
	}
	return "application/octet-stream", false
}

// Range reads up to length bytes from offset, clamped to MaxRangeBytes
// and to the end of the file. It returns what it read plus the total
// size, which is what a client needs to build a Content-Range header, and
// the type to serve it as (ServedType: never the stored mime raw).
func (sv *Server) Range(token string, offset, length int64) (content []byte, total int64, ctype string, err error) {
	stream, err := sv.Open(token)
	if err != nil {
		return nil, 0, "", err
	}
	defer stream.Close()
	ctype, _ = ServedType(stream.Mime)

	if offset < 0 || offset > stream.Size {
		return nil, stream.Size, ctype, fmt.Errorf("offset %d outside the file", offset)
	}
	if length <= 0 || length > MaxRangeBytes {
		length = MaxRangeBytes
	}
	if remaining := stream.Size - offset; length > remaining {
		length = remaining
	}

	if _, err := stream.ReadSeeker().Seek(offset, io.SeekStart); err != nil {
		return nil, stream.Size, ctype, fmt.Errorf("seeking to %d: %w", offset, err)
	}
	buf := make([]byte, length)
	// ReadFull, not Read: a single Read on a file is free to return
	// fewer bytes than asked for, which would silently truncate the span
	// the client believes it received.
	n, err := io.ReadFull(stream.ReadSeeker(), buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, stream.Size, ctype, fmt.Errorf("reading %d bytes at %d: %w", length, offset, err)
	}

	return buf[:n], stream.Size, ctype, nil
}
