// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
	"io"
)

const (
	cValidatorText = "ValidateSessionString::"

	cSaltLen = 16

	// Argon2id parameters for turning the account password into the key
	// that wraps the vault's random internal secret (see the New/ChangeKey
	// comments below for what that secret is actually used for). This only
	// ever runs once per login/password-change, not on any hot path, so
	// it's tuned for real resistance rather than speed — comfortably under
	// a second even on a Raspberry Pi.
	cArgonTime    = 3
	cArgonMemory  = 64 * 1024 // KiB, i.e. 64 MiB
	cArgonThreads = 4
	cArgonKeyLen  = 32
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Session struct {
	Uuid   string
	dao    *dao.Dao
	cipher cipher.AEAD
}

// deriveWrappingKey turns a password into the AES key that wraps the
// vault's secret blob, salted and stretched with Argon2id so that stealing
// the encrypted vault (a DB backup, a stolen disk) doesn't reduce cracking
// the password to plain unsalted-SHA256 speed.
func deriveWrappingKey(password string, salt []byte) [32]byte {
	var key [32]byte
	copy(key[:], argon2.IDKey([]byte(password), salt, cArgonTime, cArgonMemory, cArgonThreads, cArgonKeyLen))
	return key
}

// deriveWrappingKeyLegacy is the original, unsalted key derivation
// (plain SHA-256 of the password) — kept only so a vault created before
// salted derivation existed can still be opened with the password it
// already has, long enough to migrate it (see New).
func deriveWrappingKeyLegacy(password string) [32]byte {
	return sha256.Sum256([]byte(password))
}

func newGCMFromKey(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func randomSalt() ([]byte, error) {
	salt := make([]byte, cSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	return salt, nil
}

func New(userUuid, key string, create bool, dao *dao.Dao) (ses *Session, err error) {
	ses = &Session{
		Uuid: userUuid,
		dao:  dao,
	}

	defined, err := dao.IsSecretDefined()
	if err != nil {
		return nil, err
	}

	var secretValidator []byte

	if !defined {
		// Brand new device: nothing to stay compatible with, so start
		// straight on the salted scheme.
		salt, err := randomSalt()
		if err != nil {
			return nil, err
		}
		if ses.cipher, err = newGCMFromKey(deriveWrappingKey(key, salt)); err != nil {
			log.Error("error creating cipher", err)
			return nil, err
		}

		// This is the firsrt time that the user it authenticating, from
		// now on this will be the auth key
		secretValidator = []byte(cValidatorText + uuid.New().String())
		if err = dao.PersistSecret(ses.Encrypt(secretValidator), salt); err != nil {
			return nil, err
		}
	} else {
		salt, err := dao.GetSalt()
		if err != nil {
			return nil, err
		}

		if len(salt) == 0 {
			ses.cipher, err = newGCMFromKey(deriveWrappingKeyLegacy(key))
		} else {
			ses.cipher, err = newGCMFromKey(deriveWrappingKey(key, salt))
		}
		if err != nil {
			log.Error("error creating cipher", err)
			return nil, err
		}

		encText, err := dao.GetSecret()
		if err != nil {
			return nil, err
		}

		secretValidator, err = ses.Decrypt(encText)
		if err != nil || len(secretValidator) < len(cValidatorText) || string(secretValidator[:len(cValidatorText)]) != cValidatorText {
			return nil, errors.New("Invalid session")
		}

		if len(salt) == 0 {
			// Password just verified fine against the legacy (unsalted)
			// scheme — migrate this vault to the salted one now, so every
			// login from here on gets the stronger derivation. Never
			// touches actual file content: that's always keyed off the
			// random internal secret below, not the password, so nothing
			// else needs re-encrypting.
			if err := ses.migrateToSaltedVault(key, secretValidator); err != nil {
				log.Error("error migrating vault to salted key derivation (will retry next login):", err)
			}
		}
	}

	// Only used to store the secret, everything else will be encrypted
	// using the random secret in the vault, not the password directly.
	secret := secretValidator[len(cValidatorText):]

	keyHash := sha256.Sum256(secret)
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		log.Error("error getting block", err)
		return nil, err
	}

	// We replace the secret by the one in the DB
	ses.cipher, err = cipher.NewGCM(block)
	if err != nil {
		log.Error("error creating cipher", err)
		return nil, err
	}

	return ses, nil
}

// migrateToSaltedVault re-wraps the already-decrypted vault secret under a
// freshly salted Argon2id key and persists it, so a legacy (pre-salt)
// install only ever pays the unsalted-SHA256 cost once more, on the login
// that migrates it.
func (ses *Session) migrateToSaltedVault(password string, secretValidator []byte) error {
	newSalt, err := randomSalt()
	if err != nil {
		return err
	}
	newCipher, err := newGCMFromKey(deriveWrappingKey(password, newSalt))
	if err != nil {
		return err
	}

	nonce := make([]byte, newCipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}

	if err := ses.dao.UpdateSecret(newCipher.Seal(nonce, nonce, secretValidator, nil), newSalt); err != nil {
		return err
	}

	ses.cipher = newCipher
	return nil
}

// ChangeKey re-wraps the vault's secret under newKey, migrating to a fresh
// salt in the process regardless of what scheme oldKey was verified
// against — so this is also a way for a legacy vault to reach the salted
// scheme even before its next login does.
func (ses *Session) ChangeKey(oldKey, newKey string) (err error) {
	salt, err := ses.dao.GetSalt()
	if err != nil {
		return err
	}

	var oldCipher cipher.AEAD
	if len(salt) == 0 {
		oldCipher, err = newGCMFromKey(deriveWrappingKeyLegacy(oldKey))
	} else {
		oldCipher, err = newGCMFromKey(deriveWrappingKey(oldKey, salt))
	}
	if err != nil {
		log.Error("error creating cipher", err)
		return err
	}

	encText, err := ses.dao.GetSecret()
	if err != nil {
		return err
	}

	nonceSize := oldCipher.NonceSize()
	if len(encText) < nonceSize {
		return errors.New("ciphertext too short")
	}

	nonce, ciphertext := encText[:nonceSize], encText[nonceSize:]
	plainSecret, err := oldCipher.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return err
	}

	newSalt, err := randomSalt()
	if err != nil {
		return err
	}
	newCipher, err := newGCMFromKey(deriveWrappingKey(newKey, newSalt))
	if err != nil {
		log.Error("error creating cipher", err)
		return err
	}

	nonce = make([]byte, newCipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic(err)
	}

	// Seal appends the ciphertext and authentication tag
	err = ses.dao.UpdateSecret(newCipher.Seal(nonce, nonce, plainSecret, nil), newSalt)

	return
}

func (ses *Session) Encrypt(content []byte) []byte {
	// GCM requires a unique nonce per encryption
	nonce := make([]byte, ses.cipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic(err)
	}

	// Seal appends the ciphertext and authentication tag
	ciphertext := ses.cipher.Seal(nonce, nonce, content, nil)

	return ciphertext
}

func (ses *Session) Decrypt(content []byte) (plaintext []byte, err error) {
	nonceSize := ses.cipher.NonceSize()
	if len(content) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := content[:nonceSize], content[nonceSize:]
	plaintext, err = ses.cipher.Open(nil, nonce, ciphertext, nil)

	return
}
