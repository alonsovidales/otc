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
	"io"
)

const (
	cValidatorText = "ValidateSessionString::"
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Session struct {
	Uuid   string
	dao    *dao.Dao
	cipher cipher.AEAD
}

func New(userUuid, key string, create bool, dao *dao.Dao) (ses *Session, err error) {
	ses = &Session{
		Uuid: userUuid,
		dao:  dao,
	}

	// Only used to store the secret, everything will be encrypted using
	// the secret in the vault
	keyHash := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		log.Error("error getting block", err)
		return
	}

	ses.cipher, err = cipher.NewGCM(block)
	if err != nil {
		log.Error("error creating cipher", err)
		return
	}

	defined, err := dao.IsSecretDefined()
	if err != nil {
		return nil, err
	}
	if !defined {
		// This is the firsrt time that the user it authenticating,
		// from now on this will be the auth key
		secret := uuid.New()
		secretVal := cValidatorText + secret.String()

		err = dao.PersistSecret(ses.Encrypt([]byte(secretVal)))
		if err != nil {
			return nil, err
		}
	}

	encText, err := dao.GetSecret()
	if err != nil {
		return nil, err
	}

	secretValidator, err := ses.Decrypt(encText)
	if err != nil || string(secretValidator[:len(cValidatorText)]) != cValidatorText {
		return nil, errors.New("Invalid session")
	}

	secret := secretValidator[len(cValidatorText):]
	log.Debug("Secret:", string(secret))

	keyHash = sha256.Sum256([]byte(secret))
	block, err = aes.NewCipher(keyHash[:])
	if err != nil {
		log.Error("error getting block", err)
		return
	}

	// We replace the secret by the one in the DB
	ses.cipher, err = cipher.NewGCM(block)
	if err != nil {
		log.Error("error creating cipher", err)
		return
	}

	return
}

func (ses *Session) ChangeKey(oldKey, newKey string) (err error) {
	keyHash := sha256.Sum256([]byte(oldKey))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		log.Error("error getting block", err)
		return err
	}

	oldCipher, err := cipher.NewGCM(block)
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

	keyHash = sha256.Sum256([]byte(newKey))
	block, err = aes.NewCipher(keyHash[:])
	if err != nil {
		log.Error("error getting block", err)
		return err
	}

	newCipher, err := cipher.NewGCM(block)
	if err != nil {
		log.Error("error creating cipher", err)
		return err
	}

	nonce = make([]byte, newCipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic(err)
	}

	// Seal appends the ciphertext and authentication tag
	err = ses.dao.UpdateSecret(newCipher.Seal(nonce, nonce, plainSecret, nil))

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
