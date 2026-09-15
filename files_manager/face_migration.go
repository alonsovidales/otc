// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
)

// MigrateLegacyFaceEncryption re-encrypts any `faces` row still holding its
// embedding/thumbnail in plaintext.
//
// Those two columns are supposed to be encrypted at rest with the owner's
// key, same as every other piece of file content (see GetThumbnail's
// session.Decrypt and processFaces' ses.Encrypt calls) - a face crop is
// cut straight from the owner's own photo, and an embedding is a
// biometric fingerprint derived from it, so neither is any less sensitive
// than the photo itself. That encryption was missing when issue #52
// first shipped, so any face detected before this fix landed was written
// to disk as plaintext. There is no way to tell which rows are affected
// without the key (that's the whole point of the encryption), so this
// runs opportunistically on every owner login, cheap for a personal
// library's face count (see ListFaceEmbeddings' own doc comment on that),
// and self-limiting: a row that decrypts cleanly is already encrypted and
// is left untouched, so repeat runs across logins do no wasted writes.
//
// AES-GCM's authentication tag makes "is this already ciphertext?" a safe
// question to ask by simply trying to decrypt it - legacy plaintext bytes
// fail that check essentially always (forging a valid tag by accident is
// not a realistic risk), so a failed Decrypt is treated as "this is the
// old plaintext" and gets encrypted+rewritten; a row with no thumbnail
// (bbox-only) skips that column since there's nothing to encrypt.
func (mg *Manager) MigrateLegacyFaceEncryption(ses *session.Session) {
	faces, err := mg.dao.ListRawFaces()
	if err != nil {
		log.Error("face encryption migration: error listing faces:", err)
		return
	}

	migrated := 0
	for _, f := range faces {
		embedding, embErr := ses.Decrypt(f.Embedding)
		if embErr != nil {
			embedding = f.Embedding // legacy plaintext - encrypt below
		}

		var thumbnail []byte
		if len(f.Thumbnail) > 0 {
			var thumbErr error
			thumbnail, thumbErr = ses.Decrypt(f.Thumbnail)
			if thumbErr != nil {
				thumbnail = f.Thumbnail // legacy plaintext - encrypt below
			}
			if embErr == nil && thumbErr == nil {
				continue // both already encrypted - nothing to do
			}
		} else if embErr == nil {
			continue // no thumbnail to worry about, embedding already encrypted
		}

		if err := mg.dao.UpdateFaceEncryption(f.ID, ses.Encrypt(embedding), ses.Encrypt(thumbnail)); err != nil {
			log.Error("face encryption migration: error rewriting face", f.ID, ":", err)
			continue
		}
		migrated++
	}

	if migrated > 0 {
		log.Info("face encryption migration: re-encrypted", migrated, "legacy plaintext face row(s)")
	}
}
