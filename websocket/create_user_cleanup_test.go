// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/supervisor"
)

// provisioning stands in for the database a new user gets: provisioned
// and dropped are the databases each was called for.
type provisioning struct {
	fail                 bool
	provisioned, dropped []string
}

func fakeProvisioning(t *testing.T, root string) *provisioning {
	t.Helper()
	p := &provisioning{}
	savedProvision, savedDrop, savedRoot := provisionUserDatabase, dropUserDatabase, userStorageRoot
	provisionUserDatabase = func(dbName, dbUser, dbPass, deviceUuid, subdomain, bridgeSecret string) error {
		p.provisioned = append(p.provisioned, dbName)
		if p.fail {
			return errors.New("provisioning otc_x: Error 1396: Operation CREATE USER failed")
		}
		return nil
	}
	dropUserDatabase = func(dbName, dbUser string) error {
		p.dropped = append(p.dropped, dbName)
		return nil
	}
	userStorageRoot = root
	t.Cleanup(func() {
		provisionUserDatabase, dropUserDatabase, userStorageRoot = savedProvision, savedDrop, savedRoot
	})
	return p
}

func usersOwner(t *testing.T) (*connHandler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db), sup: &supervisor.Supervisor{}}}
	return ch, mock
}

func expectNewUserChecks(mock sqlmock.Sqlmock, username string) {
	mock.ExpectQuery("select count\\(\\*\\) from `users` where `username` = \\?").WithArgs(username).WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	mock.ExpectQuery("select max\\(`port`\\) from `users`").WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))
}

// A username longer than a DNS label is refused before anything is looked
// up or made - it used to fail on the users row, in MariaDB's English,
// with its database already made.
func TestCreateUserRefusesALongUsernameFirst(t *testing.T) {
	p := fakeProvisioning(t, t.TempDir())
	ch, mock := usersOwner(t)
	_, _, err := ch.createUser(&pb.ReqCreateUser{Username: strings.Repeat("k", cMaxUsername+1)})
	if got, want := devtext.Text("", err), i18n.DevUsersUsernameTooLong(63).Render(""); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(p.provisioned) != 0 {
		t.Fatal("provisioned for a name refused")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Whatever fails once a new user's database is being made - the database
// itself half made, its storage folder, the users row - takes back what
// was made: no database, MySQL user or folder is left with no row naming
// them.
func TestCreateUserTakesBackWhatAFailureLeft(t *testing.T) {
	root := t.TempDir()
	leftovers := func() []string {
		t.Helper()
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}

	// The users row fails (another tab's user took the name meanwhile,
	// or any database error).
	p := fakeProvisioning(t, root)
	ch, mock := usersOwner(t)
	expectNewUserChecks(mock, "kim")
	mock.ExpectExec("insert into `users`").WillReturnError(errors.New("Error 1062: Duplicate entry 'kim' for key 'username'"))
	if _, _, err := ch.createUser(&pb.ReqCreateUser{Username: "kim"}); err == nil {
		t.Fatal("no error")
	}
	if len(p.provisioned) != 1 || len(p.dropped) != 1 || p.dropped[0] != p.provisioned[0] {
		t.Fatalf("provisioned %v, dropped %v", p.provisioned, p.dropped)
	}
	if left := leftovers(); len(left) != 0 {
		t.Fatalf("left %v", left)
	}

	// The database half made.
	p = fakeProvisioning(t, root)
	p.fail = true
	ch, mock = usersOwner(t)
	expectNewUserChecks(mock, "ana")
	if _, _, err := ch.createUser(&pb.ReqCreateUser{Username: "ana"}); err == nil {
		t.Fatal("no error")
	}
	if len(p.dropped) != 1 || p.dropped[0] != p.provisioned[0] {
		t.Fatalf("provisioned %v, dropped %v", p.provisioned, p.dropped)
	}

	// Its storage folder can't be made.
	blocked := filepath.Join(root, "not-a-folder")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p = fakeProvisioning(t, blocked)
	ch, mock = usersOwner(t)
	expectNewUserChecks(mock, "bo")
	if _, _, err := ch.createUser(&pb.ReqCreateUser{Username: "bo"}); err == nil {
		t.Fatal("no error")
	}
	if len(p.dropped) != 1 || p.dropped[0] != p.provisioned[0] {
		t.Fatalf("provisioned %v, dropped %v", p.provisioned, p.dropped)
	}

	// Made: nothing taken back.
	os.Remove(blocked)
	p = fakeProvisioning(t, root)
	ch, mock = usersOwner(t)
	expectNewUserChecks(mock, "eva")
	mock.ExpectExec("insert into `users`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("from `users` where `uuid` = \\?").WillReturnError(errors.New("not now"))
	mock.ExpectQuery("select `uuid`, `username`, `port`, `subdomain`, `active`, `bridge_access`, `created` from `users`").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "username", "port", "subdomain", "active", "bridge_access", "created"}))
	if _, _, err := ch.createUser(&pb.ReqCreateUser{Username: "eva"}); err != nil {
		t.Fatal(err)
	}
	if len(p.dropped) != 0 {
		t.Fatalf("a user made had %v dropped", p.dropped)
	}
	if left := leftovers(); len(left) != 1 || !strings.HasPrefix(left[0], "user_") {
		t.Fatalf("storage folders %v", left)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A user with bridge access comes with a first password, or not at all:
// without one the account was anyone's who signed in first through the
// bridge. Refused before anything is looked up or registered; a user for
// the home network only may still come without one.
func TestCreateUserWithBridgeAccessNeedsAPassword(t *testing.T) {
	p := fakeProvisioning(t, t.TempDir())
	ch, mock := usersOwner(t)
	_, _, err := ch.createUser(&pb.ReqCreateUser{Username: "kim", RequestBridgeAccess: true})
	if got, want := devtext.Text("es", err), i18n.DevUsersPasswordNeeded().Render("es"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(p.provisioned) != 0 {
		t.Fatal("provisioned for a user refused")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
