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
// runs at the first owner login of each process - the first moment the
// key is in memory - and again at the next login only if a pass hit a
// database error. It used to run at every login: the apps sign in on
// every connection, so every reconnect read and decrypted the whole
// table, face crops included, often several at once. Once one pass
// succeeds nothing can need it again in this process: processFaces only
// ever stores encrypted rows, and the key (the vault's, not the
// password) never changes. It reads a page of rows at a time, and a row
// that decrypts cleanly is already encrypted and is left untouched.
//
// AES-GCM's authentication tag makes "is this already ciphertext?" a safe
// question to ask by simply trying to decrypt it - legacy plaintext bytes
// fail that check essentially always (forging a valid tag by accident is
// not a realistic risk), so a failed Decrypt is treated as "this is the
// old plaintext" and gets encrypted+rewritten; a row with no thumbnail
// (bbox-only) skips that column since there's nothing to encrypt.
func (mg *Manager) MigrateLegacyFaceEncryption(ses *session.Session) {
	if mg.faceMigDone.Load() {
		return
	}
	// Several clients signing in at once (after a bridge restart, say):
	// one pass is enough.
	if !mg.faceMigMu.TryLock() {
		return
	}
	defer mg.faceMigMu.Unlock()
	if mg.faceMigDone.Load() {
		return
	}

	migrated, failed := 0, false
	after := ""
	for {
		faces, err := mg.dao.ListRawFacesAfter(after, cFaceMigrationPage)
		if err != nil {
			log.Error("face encryption migration: error listing faces:", err)
			return
		}
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
				failed = true
				continue
			}
			migrated++
		}
		if len(faces) < cFaceMigrationPage {
			break
		}
		after = faces[len(faces)-1].ID
	}

	if migrated > 0 {
		log.Info("face encryption migration: re-encrypted", migrated, "legacy plaintext face row(s)")
	}
	// A pass that couldn't rewrite a row runs again at the next sign-in.
	mg.faceMigDone.Store(!failed)
}

// cFaceMigrationPage is how many face rows the migration reads at a time.
const cFaceMigrationPage = 256
