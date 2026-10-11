// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN): with
// migration 010's columns both languages are kept and read back - the
// account's, in its reads, the inactivity list and the export, and the
// push one with the registrations; on a schema from before 010 every write
// still lands and the languages read as "" (not known). Run it against
// both.
func TestLanguageMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cols int
	if err := db.QueryRow("select count(*) from information_schema.columns where table_schema = database() and " +
		"((table_name = 'accounts' and column_name = 'lang') or (table_name = 'push_registrations' and column_name = 'language'))").Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols == 1 {
		t.Fatal("only one of migration 010's two columns exists")
	}
	migrated := cols == 2
	t.Logf("migration 010 run: %v", migrated)
	kept := func(lang string) string {
		if migrated {
			return lang
		}
		return ""
	}

	d := NewWithDB(db)
	const id, email, domain = "langtest-acc", "langtest@example.com", "langtest.example"
	cleanup := func() {
		for _, q := range []string{"push_registrations", "push_apns_tokens", "push_fcm_tokens", "push_web_subs", "device_metrics", "devices"} {
			db.Exec("delete from `"+q+"` where `domain` = ?", domain)
		}
		db.Exec("delete from `accounts` where `id` = ?", id)
	}
	cleanup()
	defer cleanup()

	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	created := Account{ID: id, Email: email, Created: now, LastSeen: now, FreeUntil: now, Lang: "en"}
	if err := d.CreateAccount(&created); err != nil || created.Lang != kept("en") {
		t.Fatalf("creating an account with a language: %q %v", created.Lang, err)
	}
	for _, get := range []func() (*Account, error){
		func() (*Account, error) { return d.GetAccount(id) },
		func() (*Account, error) { return d.GetAccountByEmail(email) },
	} {
		if acc, err := get(); err != nil || acc == nil || acc.Lang != kept("en") || acc.Email != email {
			t.Fatalf("reading the account: %+v %v", acc, err)
		}
	}

	err = d.SetAccountLang(id, "")
	switch {
	case migrated && err != nil:
		t.Fatalf("clearing the language: %v", err)
	case !migrated && err != ErrNoLanguageColumn:
		t.Fatalf("setting a language without the column: %v", err)
	}
	if acc, err := d.GetAccount(id); err != nil || acc.Lang != "" {
		t.Fatalf("after clearing: %+v %v", acc, err)
	}
	if migrated {
		if err := d.SetAccountLang(id, "en"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.Exec("insert into `devices` (`domain`, `owner_uuid`, `secret`, `account_id`, `created`) values (?, 'owner', 'secret', ?, ?)", domain, id, now); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPushRegistrations(domain, "pub", "priv", "en", []string{"tok"}, nil, nil); err != nil {
		t.Fatalf("storing push registrations with a language: %v", err)
	}
	if lang, err := d.PushLanguageForDomain(domain); err != nil || lang != kept("en") {
		t.Fatalf("push language: %q %v", lang, err)
	}
	if pub, priv, err := d.GetVapidKeysForDomain(domain); err != nil || pub != "pub" || priv != "priv" {
		t.Fatalf("the keys stored with it: %q %q %v", pub, priv, err)
	}

	out, err := d.ExportAccount(id)
	if err != nil || out == nil || len(out.Domains) != 1 {
		t.Fatalf("export: %+v %v", out, err)
	}
	if out.Account.Language != kept("en") || out.Domains[0].PushLanguage != kept("en") || out.Domains[0].PushTokens != 1 {
		t.Errorf("export: account %q, push %q, %d tokens", out.Account.Language, out.Domains[0].PushLanguage, out.Domains[0].PushTokens)
	}

	inactive, err := d.InactiveAccounts(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range inactive {
		if a.ID == id {
			found = true
			if a.Lang != kept("en") {
				t.Errorf("inactive list: %q", a.Lang)
			}
		}
	}
	if !found {
		t.Error("the test account is not in the inactive list")
	}

	// A device that now reports none: the registration replaces it.
	if err := d.SetPushRegistrations(domain, "pub2", "priv2", "", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if lang, err := d.PushLanguageForDomain(domain); err != nil || lang != "" {
		t.Fatalf("push language replaced: %q %v", lang, err)
	}
	if lang, err := d.PushLanguageForDomain("langtest-none.example"); err != nil || lang != "" {
		t.Fatalf("a domain without registrations: %q %v", lang, err)
	}
}
