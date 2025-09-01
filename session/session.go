package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	"io"
)

const (
	cValidatorText = "ValidateSessionString"
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Session struct {
	Uuid   string
	dao    *dao.Dao
	cipher cipher.AEAD
}

func New(uuid, key string, create bool, dao *dao.Dao) (ses *Session, err error) {
	keyHash := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		log.Error("error getting block", err)
		return
	}

	cipher, err := cipher.NewGCM(block)
	if err != nil {
		log.Error("error creating cipher", err)
		return
	}

	ses = &Session{
		Uuid:   uuid,
		dao:    dao,
		cipher: cipher,
	}

	encValidator := string(ses.Encrypt([]byte(cValidatorText)))

	defined, err := dao.IsSessionDefined()
	if err != nil {
		return nil, err
	}
	if !defined {
		// This is the firsrt time that the user it authenticating,
		// from now on this will be the auth key
		err = dao.CreateSession(encValidator)
		if err != nil {
			return nil, err
		}
	}

	encText, err := dao.GetSessionCheck()
	if err != nil {
		return nil, err
	}

	validator, err := ses.Decrypt(encText)
	if err != nil || string(validator) != cValidatorText {
		return nil, errors.New("Invalid session")
	}

	return
}

func (ses *Session) Encrypt(content []byte) []byte {
	log.Debug("Encrypt")

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
	log.Debug("Decrypt")

	nonceSize := ses.cipher.NonceSize()
	if len(content) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := content[:nonceSize], content[nonceSize:]
	plaintext, err = ses.cipher.Open(nil, nonce, ciphertext, nil)

	return
}
