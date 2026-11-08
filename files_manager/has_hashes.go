// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"os"
	"regexp"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/log"
)

// cMaxHasHashes is the most hashes one HasHashes looks at; the phones ask
// 500 at a time.
const cMaxHasHashes = 1000

var reContentHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// HasHashes is free up space's check (HasHashes in messages.proto): of the
// hashes asked, those whose content this device holds whole. A phone
// deletes its own copy of a photo only when its hash is in the answer, so
// every one is looked at on the disk now - a `files` row with the hash and
// its blob at exactly the size the row's content seals to in the blob's
// own format (its first bytes: OTS1 before release 164, OTS2 since), which
// a blob cut short (or one that isn't there) never has. Each hash is
// answered once; anything that isn't a SHA-256 in hex is left out, and so
// is everything past the first cMaxHasHashes. A compressed file's original
// is never in it, though HasFile answers it held: the device keeps only
// the compressed copy.
func (mg *Manager) HasHashes(hashes []string) ([]string, error) {
	seen := map[string]bool{}
	var asked []string
	for _, h := range hashes {
		if len(asked) == cMaxHasHashes {
			break
		}
		if !seen[h] && reContentHash.MatchString(h) {
			seen[h] = true
			asked = append(asked, h)
		}
	}
	sizes, err := mg.dao.FileSizesByHash(asked)
	if err != nil {
		return nil, err
	}
	// An edit's original is held whole too, with no path (edit_image.go) -
	// but never a compressed file's (compress_job.go): its bytes are gone,
	// and the phone's copy is the only one left. Left out here, before its
	// blob is looked for, which isn't missing by mistake.
	var pathless []string
	for _, h := range asked {
		if _, ok := sizes[h]; !ok {
			pathless = append(pathless, h)
		}
	}
	if len(pathless) > 0 {
		held, err := mg.dao.HeldOriginals(pathless)
		if err != nil {
			// They count as not held: the phone keeps its copies.
			log.Error("could not look for the originals of edits:", err)
		}
		for h, o := range held {
			if !o.Removed {
				sizes[h] = o.Size
			}
		}
	}
	found := []string{}
	for _, h := range asked {
		size, ok := sizes[h]
		if ok && wholeBlob(h, size) {
			found = append(found, h)
		}
	}

	return found, nil
}

// wholeBlob: hash's blob is on the disk at the size a plain content of
// size bytes seals to in the blob's format - read from its magic, as the
// two formats' headers differ in size. As hasBlob, it records whether the
// blob is there at all for the listings (missingBlobs).
func wholeBlob(hash string, size int64) bool {
	path := blobPath(hash)
	fi, err := os.Stat(path)
	there := err == nil && fi.Size() > 0
	missingBlobs.set(hash, !there)
	if !there {
		log.Error("hash known but its content is missing:", hash)
		return false
	}
	// A blob written again between the two looks (a lost one uploaded
	// again) can only make this answer no: the phone keeps its copy.
	format, err := blobstore.FormatOf(path)
	if err != nil {
		log.Error("content of", hash, "is not an encrypted blob:", err)
		return false
	}
	if want := format.EncryptedSize(size); fi.Size() != want {
		log.Error("content of", hash, "is", fi.Size(), "bytes on the disk, not the", want, "its", size, "seal to as", format)
		return false
	}

	return true
}
