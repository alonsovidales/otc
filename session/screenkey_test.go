// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/segcrypt"
)

// screenKeys points ScreenKeyDir at a directory of the test's own.
func screenKeys(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	was := ScreenKeyDir
	ScreenKeyDir = dir
	t.Cleanup(func() { ScreenKeyDir = was })
	return dir
}

// screenSession is a signed-in session over a vault whose sealed secret
// is blob, as New leaves one.
func screenSession(t *testing.T) (*Session, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	files, err := segcrypt.NewKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return &Session{Uuid: "owner-uuid", dao: dao.NewWithDB(db), cipher: files.AEAD(), files: files, dataKey: key}, mock
}

func expectVault(mock sqlmock.Sqlmock, blob string) {
	mock.ExpectQuery("select `secret` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"secret"}).AddRow([]byte(blob)))
}

func issueScreen(t *testing.T, ses *Session, mock sqlmock.Sqlmock) string {
	t.Helper()
	expectVault(mock, "vault-1")
	key, err := IssueScreenKey(ses, PasswordGeneration())
	if err != nil {
		t.Fatalf("IssueScreenKey: %v", err)
	}
	return key
}

func redeemScreen(t *testing.T, key string, ses *Session, mock sqlmock.Sqlmock, blob string) (*Session, bool) {
	t.Helper()
	expectVault(mock, blob)
	got, gen, ok, err := RedeemScreenKey(key, ses.dao)
	if err != nil {
		t.Fatalf("RedeemScreenKey: %v", err)
	}
	if ok && gen != PasswordGeneration() {
		t.Errorf("signed in under generation %d, want %d", gen, PasswordGeneration())
	}
	return got, ok
}

// The key opens the session it was issued for - the same data key, so
// what that session sealed opens - as often as it is used, as a new
// process would find it after a restart.
func TestScreenKeyOpensTheSameSessionAgainAndAgain(t *testing.T) {
	screenKeys(t)
	ses, mock := screenSession(t)
	key := issueScreen(t, ses, mock)
	sealed := ses.Encrypt([]byte("a photo's bytes"))
	for i := 0; i < 3; i++ {
		got, ok := redeemScreen(t, key, ses, mock, "vault-1")
		if !ok {
			t.Fatalf("redemption %d refused", i+1)
		}
		if got.Uuid != "owner-uuid" {
			t.Errorf("uuid %q", got.Uuid)
		}
		plain, err := got.Decrypt(sealed)
		if err != nil || string(plain) != "a photo's bytes" {
			t.Fatalf("the redeemed session doesn't open the original's: %q %v", plain, err)
		}
		if _, err := got.FileAEAD(make([]byte, segcrypt.SaltSize)); err != nil {
			t.Fatalf("no file key: %v", err)
		}
	}
}

// Only the key it was issued with opens it: another, a cut one, the one a
// new key replaced, or after a sign-out or a password change, none.
func TestScreenKeyEnds(t *testing.T) {
	screenKeys(t)
	ses, mock := screenSession(t)
	first := issueScreen(t, ses, mock)

	for _, bad := range []string{"", "not base64!", first[:20], strings.Repeat("A", 43)} {
		if _, ok := redeemScreen(t, bad, ses, mock, "vault-1"); ok {
			t.Errorf("%q opened it", bad)
		}
	}
	second := issueScreen(t, ses, mock)
	if _, ok := redeemScreen(t, first, ses, mock, "vault-1"); ok {
		t.Error("the replaced key still opens it")
	}
	if _, ok := redeemScreen(t, second, ses, mock, "vault-1"); !ok {
		t.Fatal("the new key doesn't")
	}

	RevokeScreenKey()
	if _, ok := redeemScreen(t, second, ses, mock, "vault-1"); ok {
		t.Error("it opens after the sign-out")
	}

	third := issueScreen(t, ses, mock)
	gen := PasswordGeneration()
	PasswordChanged()
	if _, ok := redeemScreen(t, third, ses, mock, "vault-1"); ok {
		t.Error("it opens after a password change")
	}
	if _, err := IssueScreenKey(ses, gen); !errors.Is(err, ErrStaleSession) {
		t.Errorf("a sign-in from before the change got one: %v", err)
	}
}

// A vault that isn't the one it was issued with (the device reset, the
// password rewrapped it) opens nothing; one that can't be read is an
// error, and the key still works once it can.
func TestScreenKeyChecksTheVault(t *testing.T) {
	screenKeys(t)
	ses, mock := screenSession(t)
	key := issueScreen(t, ses, mock)
	if _, ok := redeemScreen(t, key, ses, mock, "vault-2"); ok {
		t.Error("another vault took it")
	}
	mock.ExpectQuery("select `secret` from `vault`").WillReturnError(errors.New("the database went away"))
	if _, _, ok, err := RedeemScreenKey(key, ses.dao); ok || err == nil {
		t.Errorf("an unreadable vault: ok %v, err %v", ok, err)
	}
	if _, ok := redeemScreen(t, key, ses, mock, "vault-1"); !ok {
		t.Error("the key went with the database's failure")
	}
}

// What is kept is sealed: the file holds neither half, is the service's
// alone, and without the directory (a container, no screen runner) no key
// is issued.
func TestScreenKeyIsKeptSealed(t *testing.T) {
	dir := screenKeys(t)
	ses, mock := screenSession(t)
	issueScreen(t, ses, mock)
	path := filepath.Join(dir, cScreenKeyFile)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	sealed, _ := os.ReadFile(path)
	if bytes.Contains(sealed, ses.dataKey) || bytes.Contains(sealed, []byte("owner-uuid")) {
		t.Error("the file holds the data key or the uuid in the clear")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("%d files left in the directory", len(entries))
	}

	ScreenKeyDir = filepath.Join(dir, "missing")
	if _, err := IssueScreenKey(ses, PasswordGeneration()); !errors.Is(err, ErrNoScreenKeys) {
		t.Errorf("without the directory: %v", err)
	}
}

// The directory is the one systemd-tmpfiles makes (screen runner).
func TestScreenKeyDirIsTheOneMadeAtBoot(t *testing.T) {
	conf, err := os.ReadFile("../scripts/screen-runner/otc-screen.conf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), "\nd "+ScreenKeyDir+" 0700 otc otc -\n") {
		t.Errorf("otc-screen.conf doesn't make %s", ScreenKeyDir)
	}
}
