// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN), at any
// step of the move to hashed secrets: before migration 013 (the plaintext
// only), between 013 and 014 (both), after 014 or a fresh db.sql (the
// hashes only). Run it against each. A device's secret is stored in what
// the schema has, checked, rotated, replaced and released by it; between
// the two steps a secret a node of an older release wrote (the plaintext
// alone) is the one that counts, never the stale hash beside it. Setup codes
// are kept by their hash and, while the schema has account_tokens, as they
// are for the older nodes too; a code only an older node stored is found
// but moves no name, except before 013, when every code is such a one.
func TestHashedSecretsMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exists := func(q string) bool {
		t.Helper()
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	column := func(c string) bool {
		return exists("select count(*) from information_schema.columns where table_schema = database() and table_name = 'devices' and column_name = '" + c + "'")
	}
	table := func(n string) bool {
		return exists("select count(*) from information_schema.tables where table_schema = database() and table_name = '" + n + "'")
	}
	plain, hashed, codes, legacy := column("secret"), column("secret_hash"), table("account_setup_codes"), table("account_tokens")
	t.Logf("devices.secret %v, devices.secret_hash %v, account_setup_codes %v, account_tokens %v", plain, hashed, codes, legacy)
	if !plain && !hashed || !codes && !legacy || plain != legacy {
		t.Fatal("a schema between the migrations' steps")
	}

	d := NewWithDB(db)
	const acc = "hashed-secret-test"
	const a, b = "hs-a.bridge.test", "hs-b.bridge.test"
	cleanup := func() {
		db.Exec("delete from `devices` where `domain` in (?, ?)", a, b)
		db.Exec("delete from `released_domains` where `domain` in (?, ?)", a, b)
		if codes {
			db.Exec("delete from `account_setup_codes` where `account_id` = ?", acc)
		}
		if legacy {
			db.Exec("delete from `account_tokens` where `account_id` = ?", acc)
		}
	}
	cleanup()
	defer cleanup()

	// stored is what devices keeps of domain's secret: the plaintext and
	// the hash, each "" when its column is missing or it is NULL.
	stored := func(domain string) (p, h string) {
		t.Helper()
		if plain {
			if err := db.QueryRow("select `secret` from `devices` where `domain` = ?", domain).Scan(&p); err != nil {
				t.Fatal(err)
			}
		}
		if hashed {
			var nh sql.NullString
			if err := db.QueryRow("select `secret_hash` from `devices` where `domain` = ?", domain).Scan(&nh); err != nil {
				t.Fatal(err)
			}
			h = nh.String
		}
		return p, h
	}
	keeps := func(domain, secret string) {
		t.Helper()
		p, h := stored(domain)
		if plain && p != secret || !plain && p != "" {
			t.Errorf("%s: plaintext %q stored, want %q (column there: %v)", domain, p, secret, plain)
		}
		if hashed && h != SecretHash(secret) {
			t.Errorf("%s: hash %q stored, want SecretHash(%q)", domain, h, secret)
		}
	}
	valid := func(owner, domain, secret string) bool {
		t.Helper()
		defined, ok, err := d.IsValidDevice(owner, domain, secret)
		if err != nil || !defined {
			t.Fatalf("checking %s: defined %v, %v", domain, defined, err)
		}
		return ok
	}

	const s1 = "0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := d.RegisterAccountDevice(acc, "owner-a", a, s1); err != nil {
		t.Fatal(err)
	}
	keeps(a, s1)
	if !valid("owner-a", a, s1) {
		t.Error("the device's own secret was refused")
	}
	for _, bad := range []struct{ owner, secret string }{{"owner-a", "wrong"}, {"owner-x", s1}, {"owner-a", ""}, {"owner-a", SecretHash(s1)}} {
		if valid(bad.owner, a, bad.secret) {
			t.Errorf("owner %q, secret %q taken for the device", bad.owner, bad.secret)
		}
	}
	if defined, ok, err := d.IsValidDevice("owner-a", "hs-none.bridge.test", s1); defined || ok || err != sql.ErrNoRows {
		t.Errorf("an unknown domain: defined %v, valid %v, %v", defined, ok, err)
	}

	const s2 = "fedcba9876543210fedcba9876543210fedcba9876543210"
	if ok, err := d.RotateSecret("owner-a", a, "wrong", s2); err != nil || ok {
		t.Fatalf("a rotation with the wrong secret: %v %v", ok, err)
	}
	if ok, err := d.RotateSecret("owner-a", a, "", s2); err != nil || ok {
		t.Fatalf("a rotation with no secret: %v %v", ok, err)
	}
	if ok, err := d.RotateSecret("owner-a", a, s1, s2); err != nil || !ok {
		t.Fatalf("a rotation: %v %v", ok, err)
	}
	keeps(a, s2)
	if !valid("owner-a", a, s2) || valid("owner-a", a, s1) {
		t.Error("after the rotation, the new secret must be the only one")
	}

	// A node of an older release rotating the secret writes the plaintext
	// alone: that one counts, the stale hash beside it no longer does.
	if plain && hashed {
		const older = "1111111111111111222222222222222233333333333333334"
		if _, err := db.Exec("update `devices` set `secret` = ? where `owner_uuid` = ? and `domain` = ? and `secret` = ?", older, "owner-a", a, s2); err != nil {
			t.Fatal(err)
		}
		if !valid("owner-a", a, older) || valid("owner-a", a, s2) {
			t.Error("an older node's rotation: its secret must be the only one")
		}
		if ok, err := d.RotateSecret("owner-a", a, older, s2); err != nil || !ok {
			t.Fatalf("rotating back from the older node's secret: %v %v", ok, err)
		}
		keeps(a, s2)
	}

	const s3 = "abababababababababababababababababababababababab"
	if ok, err := d.ReplaceDeviceIdentity("another-account", a, "owner-b", s3); err != nil || ok {
		t.Fatalf("another account's replacement: %v %v", ok, err)
	}
	if ok, err := d.ReplaceDeviceIdentity(acc, a, "owner-b", s3); err != nil || !ok {
		t.Fatalf("the replacement: %v %v", ok, err)
	}
	keeps(a, s3)
	if !valid("owner-b", a, s3) || valid("owner-a", a, s2) || valid("owner-b", a, s2) {
		t.Error("after the replacement, only the new identity may register")
	}

	if ok, err := d.ReleaseDeviceDomain("owner-b", a, "wrong"); err != nil || ok {
		t.Fatalf("a release with the wrong secret: %v %v", ok, err)
	}
	if _, h := stored(a); hashed && h == "" {
		t.Fatal("the refused release changed the device")
	}
	if ok, err := d.ReleaseDeviceDomain("owner-b", a, s3); err != nil || !ok {
		t.Fatalf("the release: %v %v", ok, err)
	}
	if registered, err := d.IsDomainRegistered(a); err != nil || registered {
		t.Fatalf("released, still registered: %v %v", registered, err)
	}

	if err := d.RegistreDevice("owner-c", b, s1); err != nil {
		t.Fatal(err)
	}
	keeps(b, s1)
	if !valid("owner-c", b, s1) || valid("owner-c", b, s2) {
		t.Error("an open registration's secret")
	}

	// Setup codes.
	now := time.Now().UTC().Truncate(time.Second)
	const code = "ABCD2345"
	if err := d.SaveSetupCode(code, SecretHash(code), acc, true, now, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if codes && !exists("select count(*) from `account_setup_codes` where `code_hash` = '"+SecretHash(code)+"' and `owner_confirmed` = 1") {
		t.Error("the code was not kept by its hash")
	}
	if legacy != exists(func() string {
		if !legacy {
			return "select 0"
		}
		return "select count(*) from `account_tokens` where `token` = '" + code + "' and `purpose` = 'setup'"
	}()) {
		t.Errorf("the code kept as it is for an older node: want %v", legacy)
	}
	if codes && exists("select count(*) from `account_setup_codes` where `code_hash` = '"+code+"'") {
		t.Error("the code itself was stored as its hash")
	}
	sc, found, err := d.SetupCodeFor(code, SecretHash(code), now)
	if err != nil || !found || sc.AccountID != acc || !sc.OwnerConfirmed {
		t.Fatalf("looking the code up: %+v %v %v", sc, found, err)
	}
	if _, found, err := d.SetupCodeFor(code, SecretHash(code), now.Add(16*time.Minute)); err != nil || found {
		t.Fatalf("an expired code: %v %v", found, err)
	}
	if err := d.SaveSetupCode("EFGH2345", SecretHash("EFGH2345"), acc, false, now, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if sc, found, err := d.SetupCodeFor("EFGH2345", SecretHash("EFGH2345"), now); err != nil || !found || sc.OwnerConfirmed != !codes {
		// Before 013 every code moves a name, as it always did.
		t.Fatalf("a code made for a session alone: %+v %v %v", sc, found, err)
	}
	if legacy {
		if _, err := db.Exec("insert into `account_tokens` (`token`, `account_id`, `purpose`, `expires`) values ('OLDN2345', ?, 'setup', ?)", acc, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		sc, found, err := d.SetupCodeFor("OLDN2345", SecretHash("OLDN2345"), now)
		if err != nil || !found || sc.AccountID != acc || sc.OwnerConfirmed != !codes {
			t.Fatalf("a code an older node made: %+v %v %v", sc, found, err)
		}
	}
	if err := d.pruneSetupCodes(now.Add(20 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := d.SetupCodeFor(code, SecretHash(code), now); err != nil || found {
		t.Fatalf("a pruned code: %v %v", found, err)
	}
	if err := d.SaveSetupCode(code, SecretHash(code), acc, true, now, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeleteAccount(acc); err != nil {
		t.Fatal(err)
	}
	if _, found, err := d.SetupCodeFor(code, SecretHash(code), now); err != nil || found {
		t.Fatalf("a code of a deleted account: %v %v", found, err)
	}
}
