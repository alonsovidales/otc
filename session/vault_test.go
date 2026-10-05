// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
)

// capture matches any argument and keeps it.
type capture struct{ v *[]byte }

func (c capture) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	*c.v = append([]byte(nil), b...)
	return ok
}

// Two first sign-ins at once both find no vault. The one whose insert
// comes second must sign in against the vault the first stored - same
// password: the same data key; another password: refused - and never keep
// a data key of its own that nobody stored.
func TestNewThatLosesTheVaultRaceUsesTheStoredVault(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)

	var secret, salt []byte
	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	mock.ExpectExec("insert into `vault`").WithArgs(capture{&secret}, capture{&salt}).WillReturnResult(sqlmock.NewResult(1, 1))
	winner, err := New("owner", "right password", true, d)
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}

	lose := func(password string) (*Session, error) {
		mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
		mock.ExpectExec("insert into `vault`").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery("select `salt` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"salt"}).AddRow(salt))
		mock.ExpectQuery("select `secret` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"secret"}).AddRow(secret))
		return New("owner", password, true, d)
	}

	loser, err := lose("right password")
	if err != nil {
		t.Fatalf("same password, lost the race: %v", err)
	}
	got, err := loser.Decrypt(winner.Encrypt([]byte("photo")))
	if err != nil || string(got) != "photo" {
		t.Fatalf("the two sessions must share the stored data key: %q, %v", got, err)
	}
	if _, err := lose("another password"); err == nil {
		t.Fatal("a different password must be refused once the vault exists")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}
