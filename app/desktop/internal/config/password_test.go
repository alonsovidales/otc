// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// A keyring that can't be asked (locked at boot) is not "no
// password": the daemon waits for it. With the password kept in the file
// (the user's choice at service install), the keyring isn't asked at all.
func TestPasswordKeyringLockedAndFile(t *testing.T) {
	withConfigDir(t)
	t.Setenv("OTC_SYNC_NO_KEYRING", "")
	keyring.MockInitWithError(errors.New("the keyring is locked"))
	t.Cleanup(keyring.MockInit)

	if _, err := LoadPassword(); !errors.Is(err, ErrKeyringLocked) {
		t.Fatalf("a locked keyring read as %v", err)
	}
	keyring.MockInit()
	if err := keyring.Set(keyringService, keyringUser, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := MovePasswordToFile("pw"); err != nil {
		t.Fatal(err)
	}
	if InKeyring() {
		t.Fatal("still in the keyring")
	}
	p, _ := PasswordPath()
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("password file: %v %v", fi, err)
	}
	keyring.MockInitWithError(errors.New("the keyring is locked"))
	if pw, err := LoadPassword(); err != nil || pw != "pw" {
		t.Fatalf("from the file: %q %v", pw, err)
	}
	cfg, _ := Load()
	if !cfg.PasswordInFile {
		t.Fatal("the choice wasn't kept")
	}
}
