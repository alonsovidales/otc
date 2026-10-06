// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/segcrypt"
)

// Issue #187: sizes were stored as int32 until release 93 widened the
// columns, so a row written before it for content of 2 GiB or more holds
// the size wrapped (negative at 2-4 GiB, a small number above). Such
// content has a blob larger than MaxInt32, and the blob's size on disk
// gives the content's (segcrypt.PlainSize) without a key, so this
// corrects those rows once, in the background at start.

// cSizeBackfillPage is how many hashes the backfill reads at a time.
const cSizeBackfillPage = 1000

// backfillSizes corrects the wrapped sizes once per database: settings.
// sizes_backfilled records that it ran. A pass that hit an error leaves it
// unset and runs again at the next start; every update only touches rows
// whose size differs, so running again changes nothing that is right.
func (mg *Manager) backfillSizes() {
	done, err := mg.dao.SizesBackfilled()
	if err != nil {
		log.Error("size backfill: could not tell whether it has run:", err)
		return
	}
	if done {
		mg.sizesBackfilled.Store(true)
		return
	}
	// Before release 93's migration a corrected size can't be stored.
	wide, err := mg.dao.SizeColumnsWide()
	if err != nil {
		log.Error("size backfill: could not check the size columns:", err)
		return
	}
	if !wide {
		log.Error("size backfill: the size columns are not BIGINT yet (release 93's migration), skipped")
		return
	}

	fixed, failed := int64(0), false
	after := ""
	for {
		hashes, err := mg.dao.ContentHashesAfter(after, cSizeBackfillPage)
		if err != nil {
			log.Error("size backfill: error listing content:", err)
			return
		}
		for _, h := range hashes {
			n, ok, err := blobContentSize(blobPath(h))
			if err == nil && ok {
				var changed int64
				changed, err = mg.dao.SetSizeOfHash(h, n)
				fixed += changed
			}
			if err != nil {
				log.Error("size backfill: content", h, ":", err)
				failed = true
			}
		}
		if len(hashes) < cSizeBackfillPage {
			break
		}
		after = hashes[len(hashes)-1]
	}

	// The owner's own posts keep a plaintext copy of each file, whose size
	// is the content's.
	hashes, err := mg.dao.OwnPublicationHashes()
	if err != nil {
		log.Error("size backfill: error listing the posts' files:", err)
		return
	}
	postsDir := cfg.GetStr("otc", "unenc-storage-path")
	for _, h := range hashes {
		n, ok, err := largeFileSize(filepath.Join(postsDir, h))
		if err == nil && ok {
			var changed int64
			changed, err = mg.dao.SetOwnPublicationSize(h, n)
			fixed += changed
		}
		if err != nil {
			log.Error("size backfill: post file", h, ":", err)
			failed = true
		}
	}

	if fixed > 0 {
		log.Info("size backfill: corrected the size of", fixed, "row(s) of 2 GiB or more")
	}
	if failed {
		return
	}
	if err := mg.dao.SetSizesBackfilled(); err != nil {
		log.Error("size backfill: could not record that it ran:", err)
		return
	}
	mg.sizesBackfilled.Store(true)
}

// largeFileSize is the size of the file at path when an int32 can't hold
// it; ok is false for a smaller or missing file.
func largeFileSize(path string) (n int64, ok bool, err error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if fi.Size() <= math.MaxInt32 {
		return 0, false, nil
	}
	return fi.Size(), true, nil
}

// blobContentSize is the content size of the encrypted blob at path when
// the blob is larger than MaxInt32 - the only ones whose row can hold a
// wrapped size. ok is false for any other blob, and for one that isn't
// segmented or whose size no segmented file has: blobstore can't read
// those either.
func blobContentSize(path string) (n int64, ok bool, err error) {
	enc, ok, err := largeFileSize(path)
	if !ok || err != nil {
		return 0, false, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil // deleted meanwhile
	}
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	head := make([]byte, segcrypt.HeaderSize)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0, false, err
	}
	if !segcrypt.IsSegmented(head) {
		return 0, false, nil
	}
	n, err = segcrypt.PlainSize(enc)
	if err != nil {
		return 0, false, nil
	}
	return n, true, nil
}
