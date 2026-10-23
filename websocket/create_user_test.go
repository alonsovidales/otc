// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/supervisor"
)

// keepArg matches any []byte argument and keeps a copy of it.
type keepArg struct{ v *[]byte }

func (k keepArg) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	*k.v = append([]byte(nil), b...)
	return ok
}

// A first password the device won't take is refused before anything is
// looked up or provisioned: no database, directory or process to undo.
func TestCreateUserRefusesABadFirstPasswordBeforeProvisioning(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db), sup: &supervisor.Supervisor{}}, privKey: priv}
	ch.setSession(newTestAuthenticatedSession(t))

	short, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &priv.PublicKey, []byte("short"), nil)
	if err != nil {
		t.Fatalf("EncryptOAEP: %v", err)
	}
	// No query is expected: one would fail with sqlmock's own error, not
	// the password's reason.
	for password, want := range map[string]string{
		string(short):      devtext.Text("", session.ErrPasswordTooShort),
		"not a ciphertext": i18n.DevAuthDecryptFailed().Render(""),
	} {
		resp, _ := ch.processAuthRequest(&pb.ReqEnvelope{
			Id:      1,
			Payload: &pb.ReqEnvelope_ReqCreateUser{ReqCreateUser: &pb.ReqCreateUser{Username: "ana", Password: []byte(password)}},
		}, "")
		if !resp.Error || resp.ErrorMessage != want || resp.Payload != nil {
			t.Errorf("expected the error %q, got %+v", want, resp)
		}
	}
	if !strings.Contains(devtext.Text("", session.ErrPasswordTooShort), "8 characters") {
		t.Errorf("the reason should say the minimum length: %q", devtext.Text("", session.ErrPasswordTooShort))
	}
}

// The vault createUser stores is the one the account's own sign-in opens:
// the first password signs in, any other is refused.
func TestNewVaultIsTheAccountsFirstSignIn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)

	var secret, salt []byte
	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	mock.ExpectExec("insert into `vault`").WithArgs(keepArg{&secret}, keepArg{&salt}).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := newVault(d, "ana-uuid", "first password"); err != nil {
		t.Fatalf("newVault: %v", err)
	}

	signIn := func(password string) error {
		mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))
		mock.ExpectQuery("select `salt` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"salt"}).AddRow(salt))
		mock.ExpectQuery("select `secret` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"secret"}).AddRow(secret))
		_, err := session.New("ana-uuid", password, false, d)
		return err
	}
	if err := signIn("first password"); err != nil {
		t.Fatalf("the first password must sign in: %v", err)
	}
	if err := signIn("someone else's"); err == nil {
		t.Fatal("another password must be refused once the first one is set")
	}
}
