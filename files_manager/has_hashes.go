// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"os"
	"regexp"

	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/segcrypt"
)

// cMaxHasHashes is the most hashes one HasHashes looks at; the phones ask
// 500 at a time.
const cMaxHasHashes = 1000

var reContentHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// HasHashes is free up space's check (HasHashes in messages.proto): of the
// hashes asked, those whose content this device holds whole. A phone
// deletes its own copy of a photo only when its hash is in the answer, so
// every one is looked at on the disk now - a `files` row with the hash and
// its blob at exactly the size the row's content seals to, which a blob
// cut short (or one that isn't there) never has. Each hash is answered
// once; anything that isn't a SHA-256 in hex is left out, and so is
// everything past the first cMaxHasHashes.
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
// size bytes seals to. As hasBlob, it records whether the blob is there at
// all for the listings (missingBlobs).
func wholeBlob(hash string, size int64) bool {
	fi, err := os.Stat(blobPath(hash))
	there := err == nil && fi.Size() > 0
	missingBlobs.set(hash, !there)
	if !there {
		log.Error("hash known but its content is missing:", hash)
		return false
	}
	if fi.Size() != segcrypt.EncryptedSize(size) {
		log.Error("content of", hash, "is", fi.Size(), "bytes on the disk, not the", segcrypt.EncryptedSize(size), "its", size, "seal to")
		return false
	}

	return true
}
