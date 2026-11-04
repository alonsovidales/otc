// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// With migration 015: a user's address is registered as its primary's,
// and a new identity for it is only ever written where it is.
func TestUserAddressStatements(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	const secret = "user-secret-0123456789abcdefghijklmnop"
	mock.ExpectExec("insert into `devices` \\(`owner_uuid`, `domain`, `account_id`, `created`, `primary_domain`, `secret`, `secret_hash`\\)").
		WithArgs("user", "pepe.test", "acc", sqlmock.AnyArg(), "cala.test", secret, SecretHash(secret)).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.RegisterUserAddress("acc", "cala.test", "user", "pepe.test", secret); err != nil {
		t.Fatal(err)
	}
	// A primary from before accounts: no account, as RegistreDevice.
	mock.ExpectExec("insert into `devices` \\(`owner_uuid`, `domain`, `primary_domain`, `secret`, `secret_hash`\\)").
		WithArgs("user", "pepa.test", "old.test", secret, SecretHash(secret)).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.RegisterUserAddress("", "old.test", "user", "pepa.test", secret); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("update `devices` set `owner_uuid` = \\?, `secret` = \\?, `secret_hash` = \\? where `domain` = \\? and `account_id` = \\? and `primary_domain` = \\?").
		WithArgs("user-2", secret, SecretHash(secret), "casa.test", "acc", "newpi.test").WillReturnResult(sqlmock.NewResult(0, 0))
	if ok, err := d.ReplaceUserAddressIdentity("acc", "newpi.test", "casa.test", "user-2", secret); ok || err != nil {
		t.Fatalf("a name that isn't newpi's user's moved: %v %v", ok, err)
	}
	mock.ExpectExec("update `devices` set `primary_domain` = \\? where `domain` = \\? and `primary_domain` is null").
		WithArgs("cala.test", "pepe.test").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.ClaimUserAddress("pepe.test", "cala.test"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("select count\\(\\*\\) from `released_domains` where `domain` = \\? and `released_at` > \\? and `account_id` = \\? and `primary_domain` = \\?").
		WithArgs("pepe.test", sqlmock.AnyArg(), "acc", "cala.test").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if held, err := d.NameHeldForPrimary("pepe.test", "acc", "cala.test"); !held || err != nil {
		t.Fatalf("held for cala's user: %v %v", held, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Before migration 015: new addresses are registered as before, and no
// name moves or comes back for a primary - nothing says whose user's it is.
func TestUserAddressesWithoutTheColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	const secret = "user-secret-0123456789abcdefghijklmnop"
	mock.ExpectExec("insert into `devices` \\(`owner_uuid`, `domain`, `account_id`, `created`, `primary_domain`,").WillReturnError(unknownColumn("primary_domain"))
	mock.ExpectExec("insert into `devices` \\(`owner_uuid`, `domain`, `account_id`, `created`, `secret`, `secret_hash`\\)").
		WithArgs("user", "pepe.test", "acc", sqlmock.AnyArg(), secret, SecretHash(secret)).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.RegisterUserAddress("acc", "cala.test", "user", "pepe.test", secret); err != nil {
		t.Fatal(err)
	}
	// Known missing now: nothing is asked.
	if ok, err := d.ReplaceUserAddressIdentity("acc", "cala.test", "pepe.test", "user-2", secret); ok || err != nil {
		t.Fatalf("moved without the column: %v %v", ok, err)
	}
	if err := d.ClaimUserAddress("pepe.test", "cala.test"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("select count\\(\\*\\) from `released_domains`").WillReturnError(unknownColumn("primary_domain"))
	if held, err := d.NameHeldForPrimary("pepe.test", "acc", "cala.test"); held || err != nil {
		t.Fatalf("held for a primary without the column: %v %v", held, err)
	}
	mock.ExpectExec("insert into `released_domains` \\(`domain`, `account_id`, `released_at`\\) values").
		WithArgs("pepe.test", "acc", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.recordRelease("pepe.test", "acc", "cala.test"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN), with
// migration 015 or without it. With it: a primary's user's address is its
// primary's, moved by that primary alone and kept so while held after its
// release; a name moved with its owner's confirmation, or whose primary is
// released, is no longer any primary's. Without it: addresses are
// registered, and none moves for a primary.
func TestUserAddressesMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	column := func(table string) bool {
		t.Helper()
		var n int
		if err := db.QueryRow("select count(*) from information_schema.columns where table_schema = database() and table_name = ? and column_name = 'primary_domain'", table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	with := column("devices")
	if with != column("released_domains") {
		t.Fatal("a schema with half of migration 015")
	}
	t.Logf("migration 015's columns: %v", with)

	d := NewWithDB(db)
	const acc = "user-address-test"
	const casa, pepe, pepa, newpi, byhand = "ua-casa.bridge.test", "ua-pepe.bridge.test", "ua-pepa.bridge.test", "ua-newpi.bridge.test", "ua-byhand.bridge.test"
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef"
	cleanup := func() {
		for _, dom := range []string{casa, pepe, pepa, newpi, byhand} {
			db.Exec("delete from `devices` where `domain` = ?", dom)
			db.Exec("delete from `released_domains` where `domain` = ?", dom)
		}
	}
	cleanup()
	defer cleanup()
	primaryOf := func(domain string) string {
		t.Helper()
		if !with {
			return ""
		}
		var p sql.NullString
		if err := db.QueryRow("select `primary_domain` from `devices` where `domain` = ?", domain).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p.String
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(d.RegisterAccountDevice(acc, "casa-owner", casa, secret))
	must(d.RegisterAccountDevice(acc, "newpi-owner", newpi, secret))
	must(d.RegisterUserAddress(acc, casa, "pepe-owner", pepe, secret))
	if defined, valid, err := d.IsValidDevice("pepe-owner", pepe, secret); !defined || !valid || err != nil {
		t.Fatalf("the user's identity: %v %v %v", defined, valid, err)
	}
	if !with {
		if ok, err := d.ReplaceUserAddressIdentity(acc, casa, pepe, "pepe-2", secret); ok || err != nil {
			t.Fatalf("moved without the column: %v %v", ok, err)
		}
		if ok, err := d.DeleteAccountDomain(acc, pepe); !ok || err != nil {
			t.Fatal(ok, err)
		}
		if held, err := d.NameHeldForPrimary(pepe, acc, casa); held || err != nil {
			t.Fatalf("held for casa without the column: %v %v", held, err)
		}
		return
	}

	if got := primaryOf(pepe); got != casa {
		t.Fatalf("pepe registered as %q's", got)
	}
	// Only casa moves pepe, and nobody moves casa or newpi this way.
	for _, c := range []struct{ primary, domain string }{{newpi, pepe}, {newpi, casa}, {casa, newpi}, {casa, casa}} {
		if ok, err := d.ReplaceUserAddressIdentity(acc, c.primary, c.domain, "someone-else", secret); ok || err != nil {
			t.Errorf("%s moved %s: %v %v", c.primary, c.domain, ok, err)
		}
	}
	if ok, err := d.ReplaceUserAddressIdentity("another-account", casa, pepe, "someone-else", secret); ok || err != nil {
		t.Errorf("moved for another account: %v %v", ok, err)
	}
	if ok, err := d.ReplaceUserAddressIdentity(acc, casa, pepe, "pepe-2", secret); !ok || err != nil {
		t.Fatalf("casa moving its user: %v %v", ok, err)
	}
	if defined, valid, _ := d.IsValidDevice("pepe-2", pepe, secret); !defined || !valid || primaryOf(pepe) != casa {
		t.Fatal("pepe's new identity, still casa's user's")
	}

	// Recorded only where nothing is.
	must(d.ClaimUserAddress(pepe, newpi))
	if got := primaryOf(pepe); got != casa {
		t.Fatalf("another primary recorded over casa: %q", got)
	}

	// Released by its user's identity: held for the account, as casa's
	// user's.
	if ok, err := d.ReleaseDeviceDomain("pepe-2", pepe, secret); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if held, err := d.NameHeldForPrimary(pepe, acc, casa); !held || err != nil {
		t.Fatalf("held for casa's user: %v %v", held, err)
	}
	if held, _ := d.NameHeldForPrimary(pepe, acc, newpi); held {
		t.Fatal("held for newpi's user")
	}
	// Registered again, and released from the account page: the same.
	must(d.RegisterUserAddress(acc, casa, "pepe-3", pepe, secret))
	if ok, err := d.DeleteAccountDomain(acc, pepe); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if held, err := d.NameHeldForPrimary(pepe, acc, casa); !held || err != nil {
		t.Fatalf("held for casa's user after the account page: %v %v", held, err)
	}

	// A device name moved with its owner's confirmation is nobody's user's.
	must(d.RegisterAccountDevice(acc, "byhand-owner", byhand, secret))
	must(d.ClaimUserAddress(byhand, casa))
	if ok, err := d.ReplaceDeviceIdentity(acc, byhand, "byhand-2", secret); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if got := primaryOf(byhand); got != "" {
		t.Fatalf("moved with its owner's confirmation, still %q's", got)
	}

	// casa released: its users' addresses, registered or held, are no
	// longer any device's to move - whoever registers casa next isn't it.
	must(d.RegisterUserAddress(acc, casa, "pepe-4", pepe, secret))
	db.Exec("delete from `released_domains` where `domain` = ?", pepe)
	must(d.recordRelease(pepa, acc, casa)) // a released user of casa's
	if ok, err := d.DeleteAccountDomain(acc, casa); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if got := primaryOf(pepe); got != "" {
		t.Fatalf("casa released, pepe still %q's", got)
	}
	if held, _ := d.NameHeldForPrimary(pepa, acc, casa); held {
		t.Fatal("casa released, a held name still its user's")
	}
	// A hold past cNameHold is none.
	db.Exec("update `released_domains` set `primary_domain` = ?, `released_at` = ? where `domain` = ?", casa, time.Now().Add(-cNameHold-time.Hour), pepa)
	if held, _ := d.NameHeldForPrimary(pepa, acc, casa); held {
		t.Fatal("an old hold counted")
	}
}
