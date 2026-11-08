// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Settings › Largest files (ListLargestFiles in messages.proto): every
// content in the library by its size, the largest first, so the owner can
// see what takes the space - and compress it from there (compress.go).
// Each content is one entry however many paths hold it, at its first path,
// with File.copies; a page ends where the next starts by (size, hash), the
// token "<size>:<hash>", so pages are read afresh each time and never held.

const (
	// cLargestPage is a page when the request says nothing; cLargestMax the
	// most one may ask.
	cLargestPage = 50
	cLargestMax  = 200
)

// largestToken is the token of the page after next.
func largestToken(next dao.LargestAfter) string {
	return strconv.FormatInt(next.Size, 10) + ":" + next.Hash
}

// parseLargestToken is where the page after token starts: the first page
// for "" and for a token that doesn't parse (an app's old token, or none
// of the device's) - the list then starts again, never fails. The hash is
// taken as it is: a row's hash need not be a SHA-256 for the pages to go
// on past it.
func parseLargestToken(token string) dao.LargestAfter {
	size, hash, ok := strings.Cut(token, ":")
	if !ok {
		return dao.LargestAfter{First: true}
	}
	n, err := strconv.ParseInt(size, 10, 64)
	if err != nil || n < 0 {
		return dao.LargestAfter{First: true}
	}
	return dao.LargestAfter{Size: n, Hash: hash}
}

// largestLimit is a page's size for limit as asked: 1-200, 0 the default.
func largestLimit(limit int32) int {
	switch {
	case limit <= 0:
		return cLargestPage
	case limit > cLargestMax:
		return cLargestMax
	}
	return int(limit)
}

// ListLargestFiles is a page of the library's largest files after token
// (ListLargestFiles in messages.proto), without their content, and the
// next page's token ("" once the list is over).
func (mg *Manager) ListLargestFiles(token string, limit int32) ([]*pb.File, string, error) {
	files, next, more, err := mg.dao.LargestFiles(parseLargestToken(token), largestLimit(limit))
	if err != nil {
		return nil, "", err
	}
	if err := mg.setCopies(files); err != nil {
		// The page goes without them, as Images' does.
		log.Error("could not count the copies of the largest files:", err)
	}
	// The viewer's Revert to original, as in Files.
	mg.setOriginals(files)
	if !more {
		return files, "", nil
	}
	return files, largestToken(next), nil
}
