// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/segcrypt"
)

// Issue #203: a session seals new files under keys of their own derived
// from its data key, and its data-key cipher is still the one files
// written before release 164 were sealed with (SHA-256 of the vault's
// secret) - so a session signed in again opens both.
func TestSessionFileKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)
	var vault, salt []byte
	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	mock.ExpectExec("insert into `vault`").WithArgs(capture{&vault}, capture{&salt}).WillReturnResult(sqlmock.NewResult(1, 1))
	first, err := New("owner", "right password", true, d)
	if err != nil {
		t.Fatal(err)
	}

	var enc bytes.Buffer
	w, err := segcrypt.NewWriter(&enc, first)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("photo"))
	w.Close()
	if f, _ := segcrypt.FormatOf(enc.Bytes()); f != segcrypt.OTS2 {
		t.Fatalf("a session writes %v", f)
	}

	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))
	mock.ExpectQuery("select `salt` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"salt"}).AddRow(salt))
	mock.ExpectQuery("select `secret` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"secret"}).AddRow(vault))
	again, err := New("owner", "right password", false, d)
	if err != nil {
		t.Fatal(err)
	}
	r, err := segcrypt.NewReader(bytes.NewReader(enc.Bytes()), int64(enc.Len()), again)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(r.Stream()); err != nil || string(got) != "photo" {
		t.Fatalf("signed in again: %q, %v", got, err)
	}

	// The data key is SHA-256 of the vault's secret, as it always was.
	wrap, _ := newGCMFromKey(deriveWrappingKey("right password", salt))
	validator, err := wrap.Open(nil, vault[:wrap.NonceSize()], vault[wrap.NonceSize():], nil)
	if err != nil {
		t.Fatal(err)
	}
	dataKey := sha256.Sum256(validator[len(cValidatorText):])
	k, _ := segcrypt.NewKey(dataKey[:])
	n := make([]byte, 12)
	if _, err := again.AEAD().Open(nil, n, k.AEAD().Seal(nil, n, []byte("x"), nil), nil); err != nil {
		t.Error("the session's cipher isn't the data key's")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	// A session that never signed in has no key to derive from.
	if _, err := (&Session{}).FileAEAD(make([]byte, segcrypt.SaltSize)); err == nil {
		t.Error("a session without a data key derived a file key")
	}
}
