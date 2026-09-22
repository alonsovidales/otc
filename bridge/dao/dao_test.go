// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/push"
)

// PruneOldLogs is the only thing standing between auth_events (which holds
// remote IP addresses) and device_metrics accumulating forever with no
// retention policy - worth pinning down that it actually issues both
// deletes, with the cutoff it was given.
func TestPruneOldLogsDeletesBothTables(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec("delete from `auth_events` where `dt` < \\?").
		WithArgs(before).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("delete from `device_metrics` where `hour_bucket` < \\?").
		WithArgs(before).
		WillReturnResult(sqlmock.NewResult(0, 5))

	d := NewWithDB(db)
	if err := d.PruneOldLogs(before); err != nil {
		t.Fatalf("PruneOldLogs returned an error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// SetPushRegistrations replaces a domain's whole snapshot (vapid keys,
// apns tokens, web push subs) in one transaction - issue #62 relies on
// this never leaving a half-replaced set for a concurrent read to observe,
// and on it actually deleting the old rows rather than only ever adding.
func TestSetPushRegistrationsReplacesWholeSnapshotInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("insert into `push_registrations`").
		WithArgs("pit.otc", "pub-key", "priv-key").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("delete from `push_apns_tokens` where `domain` = \\?").
		WithArgs("pit.otc").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("insert into `push_apns_tokens`").
		WithArgs("pit.otc", "tok-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("delete from `push_web_subs` where `domain` = \\?").
		WithArgs("pit.otc").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("insert into `push_web_subs`").
		WithArgs("pit.otc", "https://push.example/ep", "p256dh-val", "auth-val").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	d := NewWithDB(db)
	err = d.SetPushRegistrations("pit.otc", "pub-key", "priv-key",
		[]string{"tok-1"},
		[]push.WebPushSubscription{{Endpoint: "https://push.example/ep", P256dh: "p256dh-val", Auth: "auth-val"}},
	)
	if err != nil {
		t.Fatalf("SetPushRegistrations returned an error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// A failure partway through must roll back rather than leave, say, the
// apns tokens cleared but the web push subs untouched.
func TestSetPushRegistrationsRollsBackOnFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("insert into `push_registrations`").
		WithArgs("pit.otc", "pub-key", "priv-key").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("delete from `push_apns_tokens` where `domain` = \\?").
		WithArgs("pit.otc").
		WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectRollback()

	d := NewWithDB(db)
	if err := d.SetPushRegistrations("pit.otc", "pub-key", "priv-key", nil, nil); err == nil {
		t.Fatal("expected an error when a statement mid-transaction fails")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran (rollback missing?): %v", err)
	}
}

func TestGetVapidKeysForDomainReturnsEmptyWhenNoRowYet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `vapid_public_key`, `vapid_private_key` from `push_registrations`").
		WithArgs("new-domain.otc").
		WillReturnError(sql.ErrNoRows)

	d := NewWithDB(db)
	pub, priv, err := d.GetVapidKeysForDomain("new-domain.otc")
	if err != nil {
		t.Fatalf("expected no error for a not-yet-synced domain, got: %v", err)
	}
	if pub != "" || priv != "" {
		t.Errorf("expected empty keys, got pub=%q priv=%q", pub, priv)
	}
}

func TestListWebPushSubscriptionsForDomainScopesToOneDomain(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"endpoint", "p256dh", "auth"}).
		AddRow("https://push.example/a", "p256-a", "auth-a")
	mock.ExpectQuery("select `endpoint`, `p256dh`, `auth` from `push_web_subs` where `domain` = \\?").
		WithArgs("pit.otc").
		WillReturnRows(rows)

	d := NewWithDB(db)
	subs, err := d.ListWebPushSubscriptionsForDomain("pit.otc")
	if err != nil {
		t.Fatalf("ListWebPushSubscriptionsForDomain returned an error: %v", err)
	}
	if len(subs) != 1 || subs[0].Endpoint != "https://push.example/a" {
		t.Errorf("unexpected subs: %+v", subs)
	}
}

// Issue #93: SetDeviceDisabled/IsDeviceDisabled are the bridge's own
// record of a domain's disabled state - the one thing left standing once
// that domain's own device process is actually stopped (see #90).
func TestSetDeviceDisabledUpdatesTheDevicesRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `devices` set `disabled` = \\? where `domain` = \\?").
		WithArgs(true, "someone.off-the.cloud").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.SetDeviceDisabled("someone.off-the.cloud", true); err != nil {
		t.Fatalf("SetDeviceDisabled returned an error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestIsDeviceDisabledReturnsTheStoredFlag(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
		WithArgs("someone.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(true))

	d := NewWithDB(db)
	disabled, err := d.IsDeviceDisabled("someone.off-the.cloud")
	if err != nil {
		t.Fatalf("IsDeviceDisabled returned an error: %v", err)
	}
	if !disabled {
		t.Error("expected disabled=true")
	}
}

// A domain that isn't registered at all must read as "not disabled", not
// as an error - see IsDeviceDisabled's own doc comment on why an unknown
// domain shouldn't get an "account disabled" message (its connection
// attempt already fails for an unrelated reason).
func TestIsDeviceDisabledReturnsFalseForUnknownDomain(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
		WithArgs("nobody.off-the.cloud").
		WillReturnError(sql.ErrNoRows)

	d := NewWithDB(db)
	disabled, err := d.IsDeviceDisabled("nobody.off-the.cloud")
	if err != nil {
		t.Fatalf("IsDeviceDisabled returned an error: %v", err)
	}
	if disabled {
		t.Error("expected disabled=false for an unregistered domain")
	}
}

// Issue #103: IsDomainRegistered is what stops a device provisioning an
// additional user onto a subdomain that is already spoken for - a name
// registered by anything else can never be claimed by a different
// owner/secret (see IsValidDevice), so "is a row here at all?" is exactly
// the right question.
func TestIsDomainRegisteredReportsAnExistingClaim(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select 1 from `devices` where `domain` = \\?").
		WithArgs("pepe.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	d := NewWithDB(db)
	registered, err := d.IsDomainRegistered("pepe.off-the.cloud")
	if err != nil {
		t.Fatalf("IsDomainRegistered returned an error: %v", err)
	}
	if !registered {
		t.Error("expected an existing row to count as registered")
	}
}

func TestIsDomainRegisteredReportsAFreeDomain(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select 1 from `devices` where `domain` = \\?").
		WithArgs("brand-new.off-the.cloud").
		WillReturnError(sql.ErrNoRows)

	d := NewWithDB(db)
	registered, err := d.IsDomainRegistered("brand-new.off-the.cloud")
	if err != nil {
		t.Fatalf("expected no error for an unregistered domain, got: %v", err)
	}
	if registered {
		t.Error("expected an unregistered domain to be free")
	}
}

// A disabled registration still occupies the name - the row is what blocks
// a new claim, not its state.
func TestIsDomainRegisteredCountsADisabledRegistration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select 1 from `devices` where `domain` = \\?").
		WithArgs("flus.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	d := NewWithDB(db)
	registered, err := d.IsDomainRegistered("flus.off-the.cloud")
	if err != nil {
		t.Fatalf("IsDomainRegistered returned an error: %v", err)
	}
	if !registered {
		t.Error("expected a disabled device's domain to still count as taken")
	}
}

// Issue #38: the setup hand-off lives in the database, not in one bridge
// process's memory, and is answerable for ten minutes only.
func TestSetupBeaconRoundTrip(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectExec("delete from `setup_beacons` where `created` < now\\(\\) - interval \\? minute").
		WithArgs(10).WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("insert into `setup_beacons`").
		WithArgs("tok-1", "192.168.1.20").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `addr` from `setup_beacons` where `token` = \\? and `created` >= now\\(\\) - interval \\? minute").
		WithArgs("tok-1", 10).WillReturnRows(sqlmock.NewRows([]string{"addr"}).AddRow("192.168.1.20"))
	mock.ExpectQuery("select `addr` from `setup_beacons`").
		WithArgs("tok-2", 10).WillReturnRows(sqlmock.NewRows([]string{"addr"}))

	d := NewWithDB(db)
	if err := d.SetSetupBeacon("tok-1", "192.168.1.20"); err != nil {
		t.Fatalf("SetSetupBeacon: %v", err)
	}
	addr, found, err := d.GetSetupBeacon("tok-1")
	if err != nil || !found || addr != "192.168.1.20" {
		t.Errorf("GetSetupBeacon(tok-1) = %q %v %v, want the reported address", addr, found, err)
	}
	if _, found, err := d.GetSetupBeacon("tok-2"); err != nil || found {
		t.Errorf("GetSetupBeacon(tok-2): found=%v err=%v, want not found", found, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}
