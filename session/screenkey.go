// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/segcrypt"
)

// A screen plugged into the device shows the web app on it
// (scripts/screen-runner): a photo frame on a wall, with nobody there to
// type the password again. A session token (tokens.go) can't keep it
// signed in: it lasts an hour, and every token is gone when the service
// restarts, which an update does.
//
// A screen key does. Issued to a session signed in on that screen (the
// websocket package checks the connection is the device's own browser),
// it is 32 random bytes the screen's browser keeps. The device keeps the
// session's data key sealed with it (AES-256-GCM) in ScreenKeyDir, which
// is in /run: memory, never the SD card or the disks (swap is RAM only
// too, issue #156). So the screen stays signed in through the service
// restarting, and a device that was switched off keeps nothing that opens
// anything: the browser's half alone is random bytes. Neither half is
// worth anything without the other.
//
// One per device, the latest replacing the one before. Redeeming it
// doesn't use it up, and it never expires: it ends with a sign-out on the
// screen (RevokeScreenKey), a password change (PasswordChanged), a vault
// that isn't the one it was issued for (a reset), or the device switching
// off.

// ScreenKeyDir is where the sealed key lives: /run/otc-screen-key, made at
// every boot by systemd-tmpfiles (scripts/screen-runner/otc-screen.conf),
// the otc user's alone. Without it (a container, a device without the
// screen runner) no screen key is issued. A var for tests.
var ScreenKeyDir = "/run/otc-screen-key"

const (
	cScreenKeyFile  = "key"
	cScreenKeyBytes = 32
	// cScreenKeyAD binds the sealed file to what it is.
	cScreenKeyAD = "otc screen key v1" // i18n-ignore: a constant the sealed file is bound to, never shown
)

// ErrNoScreenKeys: this device keeps no screen keys (no ScreenKeyDir).
var ErrNoScreenKeys = errors.New("this device keeps no screen keys") // i18n-ignore: answered as dev.auth.token_failed

// screenKeyFile is what the sealed file holds.
type screenKeyFile struct {
	Uuid    string `json:"uuid"`
	DataKey []byte `json:"data_key"`
	// Vault is the SHA-256 of the vault's sealed secret it was issued
	// with: a reset makes a new one, and a password change rewraps it.
	Vault []byte `json:"vault"`
}

func screenKeyPath() string { return filepath.Join(ScreenKeyDir, cScreenKeyFile) }

func screenGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// vaultHash is the SHA-256 of the vault's sealed secret.
func vaultHash(d *dao.Dao) ([]byte, error) {
	enc, err := d.GetSecret()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(enc)
	return sum[:], nil
}

// IssueScreenKey makes a screen key for ses, signed in under the password
// generation gen, in place of the one before. ErrStaleSession for a
// sign-in from before a password change, ErrNoScreenKeys on a device that
// keeps none.
func IssueScreenKey(ses *Session, gen uint64) (string, error) {
	if len(ses.dataKey) != cScreenKeyBytes || ses.dao == nil {
		return "", errors.New("this session has no data key to keep") // i18n-ignore: answered as dev.auth.token_failed
	}
	if st, err := os.Stat(ScreenKeyDir); err != nil || !st.IsDir() {
		return "", ErrNoScreenKeys
	}
	vault, err := vaultHash(ses.dao)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(screenKeyFile{Uuid: ses.Uuid, DataKey: ses.dataKey, Vault: vault})
	if err != nil {
		return "", err
	}
	key := make([]byte, cScreenKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	gcm, err := screenGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plain, []byte(cScreenKeyAD))

	// Under tokenMu, as a password change is: one either finds this key
	// written, and removes it, or comes first and none is written.
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if gen != passwordGen {
		return "", ErrStaleSession
	}
	if err := writeScreenKeyLocked(sealed); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key), nil
}

// writeScreenKeyLocked replaces the sealed file whole (a temporary file,
// then a rename): 0600, never half written.
func writeScreenKeyLocked(sealed []byte) error {
	tmp, err := os.CreateTemp(ScreenKeyDir, cScreenKeyFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), screenKeyPath())
}

// RedeemScreenKey is the session key opens and the password generation it
// signs in under; ok false when key opens nothing (none kept, another
// one, a password change, a reset). err is the vault that couldn't be
// read, no verdict on the key.
func RedeemScreenKey(key string, d *dao.Dao) (ses *Session, gen uint64, ok bool, err error) {
	raw, decErr := base64.RawURLEncoding.DecodeString(key)
	if decErr != nil || len(raw) != cScreenKeyBytes || d == nil {
		return nil, 0, false, nil
	}
	tokenMu.Lock()
	gen = passwordGen
	sealed, readErr := os.ReadFile(screenKeyPath())
	tokenMu.Unlock()
	if readErr != nil {
		return nil, 0, false, nil
	}
	gcm, err := screenGCM(raw)
	if err != nil || len(sealed) < gcm.NonceSize() {
		return nil, 0, false, nil
	}
	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, openErr := gcm.Open(nil, nonce, body, []byte(cScreenKeyAD))
	if openErr != nil {
		return nil, 0, false, nil
	}
	var f screenKeyFile
	if json.Unmarshal(plain, &f) != nil || len(f.DataKey) != cScreenKeyBytes {
		return nil, 0, false, nil
	}
	vault, err := vaultHash(d)
	if err != nil {
		return nil, 0, false, err
	}
	if !bytes.Equal(vault, f.Vault) {
		return nil, 0, false, nil
	}
	files, err := segcrypt.NewKey(f.DataKey)
	if err != nil {
		return nil, 0, false, nil
	}
	return &Session{Uuid: f.Uuid, dao: d, cipher: files.AEAD(), files: files, dataKey: f.DataKey}, gen, true, nil
}

// RevokeScreenKey removes the screen key: the screen signed out.
func RevokeScreenKey() {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	removeScreenKeyLocked()
}

func removeScreenKeyLocked() {
	_ = os.Remove(screenKeyPath())
}
