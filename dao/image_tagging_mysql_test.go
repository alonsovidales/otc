// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
)

// Image Tagging's default against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN,
// as out_of_images_mysql_test.go): before release 158's column the default
// can't be applied and the switch still works; with it the device's
// default moves the switch until the user sets it, and never after.
func TestImageTaggingDefaultMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	for _, q := range []string{
		"create temporary table `settings` (`image_tagging_enabled` tinyint(1) not null default 1) engine=InnoDB",
		"insert into `settings` () values ()",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	enabled := func() bool {
		t.Helper()
		on, err := d.GetImageTaggingEnabled()
		if err != nil {
			t.Fatal(err)
		}
		return on
	}
	if _, err := d.ApplyImageTaggingDefault(false); !errors.Is(err, ErrSchemaBehind) {
		t.Fatalf("before the column: %v", err)
	}
	if err := d.SetImageTaggingEnabled(false); err != nil || enabled() {
		t.Fatalf("the switch before the column: %v, on %v", err, enabled())
	}
	if err := d.SetImageTaggingEnabled(true); err != nil || !enabled() {
		t.Fatalf("on again: %v", err)
	}

	if _, err := db.Exec("alter table `settings` add column `image_tagging_chosen` tinyint(1) not null default 0"); err != nil {
		t.Fatal(err)
	}
	// Not chosen: a low-memory device turns it off, once; a bigger one on.
	if changed, err := d.ApplyImageTaggingDefault(false); err != nil || !changed || enabled() {
		t.Fatalf("low-memory default: changed %v, %v, on %v", changed, err, enabled())
	}
	if changed, err := d.ApplyImageTaggingDefault(false); err != nil || changed {
		t.Fatalf("the same default again: changed %v, %v", changed, err)
	}
	if changed, err := d.ApplyImageTaggingDefault(true); err != nil || !changed || !enabled() {
		t.Fatalf("a bigger device's default: changed %v, %v", changed, err)
	}
	// The user's choice stays, whatever the device.
	if err := d.SetImageTaggingEnabled(true); err != nil {
		t.Fatal(err)
	}
	if changed, err := d.ApplyImageTaggingDefault(false); err != nil || changed || !enabled() {
		t.Fatalf("the user's on on a low-memory device: changed %v, %v, on %v", changed, err, enabled())
	}
	if err := d.SetImageTaggingEnabled(false); err != nil {
		t.Fatal(err)
	}
	if changed, err := d.ApplyImageTaggingDefault(true); err != nil || changed || enabled() {
		t.Fatalf("the user's off on a bigger device: changed %v, %v, on %v", changed, err, enabled())
	}

	// A 4 GB device that ran release 160, which turned it off at every
	// start whatever was chosen, leaving the choice as it was: release
	// 164's default there (off unless chosen) keeps it off, chosen or not,
	// until its user turns it on - which then stays through restarts.
	for _, chosen := range []bool{false, true} {
		if _, err := db.Exec("update `settings` set `image_tagging_enabled` = 1, `image_tagging_chosen` = ?", chosen); err != nil {
			t.Fatal(err)
		}
		if changed, err := d.TurnOffImageTagging(); err != nil || !changed || enabled() {
			t.Fatalf("release 160's start (chosen %v): changed %v, %v, on %v", chosen, changed, err, enabled())
		}
		if changed, err := d.ApplyImageTaggingDefault(false); err != nil || changed || enabled() {
			t.Fatalf("release 164's start after 160 (chosen %v): changed %v, %v, on %v", chosen, changed, err, enabled())
		}
	}
	if err := d.SetImageTaggingEnabled(true); err != nil {
		t.Fatal(err)
	}
	for start := 1; start <= 2; start++ {
		if changed, err := d.ApplyImageTaggingDefault(false); err != nil || changed || !enabled() {
			t.Fatalf("start %d after its user turned it on: changed %v, %v, on %v", start, changed, err, enabled())
		}
	}
}
