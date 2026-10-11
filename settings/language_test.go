// SPDX-License-Identifier: AGPL-3.0-or-later

package settings

import (
	"errors"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/go-sql-driver/mysql"
)

var errUnknownLanguageColumn = &mysql.MySQLError{Number: 1054, Message: "Unknown column 'language' in 'field list'"}

func expectInit(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("select `subdomain`, `device_uuid`, `bridge_secret` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"subdomain", "device_uuid", "bridge_secret"}).AddRow("a.off-the.cloud", "u", "s"))
	mock.ExpectQuery("select `face_recognition_enabled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"face_recognition_enabled"}).AddRow(false))
	mock.ExpectQuery("select `image_tagging_enabled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"image_tagging_enabled"}).AddRow(true))
}

// Init reads the language with the other settings, and on a database
// release 118's script hasn't reached starts with Automatic instead of
// failing the start.
func TestInitReadsTheLanguage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	expectInit(mock)
	mock.ExpectQuery("select `language`, `last_ui_language` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"language", "last_ui_language"}).AddRow("es", "en"))
	st, err := Init(dao.NewWithDB(db))
	if err != nil {
		t.Fatal(err)
	}
	if st.Language() != "es" || st.LastUILanguage() != "en" || st.PushLanguage() != "es" {
		t.Errorf("got %q %q %q", st.Language(), st.LastUILanguage(), st.PushLanguage())
	}

	expectInit(mock)
	mock.ExpectQuery("select `language`, `last_ui_language` from `settings`").WillReturnError(errUnknownLanguageColumn)
	st, err = Init(dao.NewWithDB(db))
	if err != nil {
		t.Fatalf("older schema: %v", err)
	}
	if st.Language() != "" || st.LastUILanguage() != "" || st.PushLanguage() != "en" {
		t.Errorf("older schema: got %q %q %q", st.Language(), st.LastUILanguage(), st.PushLanguage())
	}

	// Any other failure fails the start, as the other settings' do.
	expectInit(mock)
	mock.ExpectQuery("select `language`, `last_ui_language` from `settings`").WillReturnError(errors.New("connection lost"))
	if _, err := Init(dao.NewWithDB(db)); err == nil {
		t.Error("expected the error")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func ptr(s string) *string { return &s }

// SetLanguage stores "" or two or three lowercase letters, only while the
// stored value is still the one the app saw, and writes nothing that
// wouldn't change.
func TestSetLanguage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := &Settings{dao: dao.NewWithDB(db)}

	for _, bad := range []string{"EN", "e", "engl", "es-ES", "es_ES", "e1", " es", "es ", "ñe", "еs"} {
		if _, err := st.SetLanguage(bad, nil); !errors.Is(err, ErrInvalidLanguage) {
			t.Errorf("%q: %v", bad, err)
		}
	}

	// No expected: written as it comes. A code this build has no text for
	// is kept too (an app newer than the device).
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("ja").WillReturnResult(sqlmock.NewResult(0, 1))
	if changed, err := st.SetLanguage("ja", nil); err != nil || !changed || st.Language() != "ja" {
		t.Fatalf("no expected: %v %v %q", changed, err, st.Language())
	}

	// expected differs: nothing written, the stored value stays.
	if changed, err := st.SetLanguage("fr", ptr("es")); !errors.Is(err, ErrLanguageChanged) || changed || st.Language() != "ja" {
		t.Errorf("stale expected: %v %v %q", changed, err, st.Language())
	}
	// expected "" is a value too: Automatic, which this no longer is.
	if _, err := st.SetLanguage("fr", ptr("")); !errors.Is(err, ErrLanguageChanged) {
		t.Errorf("expected Automatic: %v", err)
	}

	// expected matches.
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("").WillReturnResult(sqlmock.NewResult(0, 1))
	if changed, err := st.SetLanguage("", ptr("ja")); err != nil || !changed || st.Language() != "" {
		t.Errorf("back to Automatic: %v %v %q", changed, err, st.Language())
	}

	// The same value again: ok, no write.
	if changed, err := st.SetLanguage("", ptr("")); err != nil || changed {
		t.Errorf("unchanged: %v %v", changed, err)
	}

	// A failed write changes nothing.
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("de").WillReturnError(errors.New("connection lost"))
	if changed, err := st.SetLanguage("de", nil); err == nil || changed || st.Language() != "" {
		t.Errorf("failed write: %v %v %q", changed, err, st.Language())
	}

	// Without the column: kept in memory, answered ok.
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("de").WillReturnError(errUnknownLanguageColumn)
	if changed, err := st.SetLanguage("de", nil); err != nil || !changed || st.Language() != "de" {
		t.Errorf("older schema: %v %v %q", changed, err, st.Language())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Two apps changing the language at once from the same value: one is
// stored, the other is told it changed - never both written, never the
// later one silently overwriting the first. Run with -race.
func TestSetLanguageConcurrentCompareAndSet(t *testing.T) {
	for round := 0; round < 50; round++ {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		mock.MatchExpectationsInOrder(false)
		// Exactly one write is expected; a second would fail the round.
		mock.ExpectExec("update `settings` set `language` = \\?").WillReturnResult(sqlmock.NewResult(0, 1))
		st := &Settings{dao: dao.NewWithDB(db)}

		langs := []string{"es", "fr"}
		errs := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range langs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, errs[i] = st.SetLanguage(langs[i], ptr(""))
			}(i)
		}
		close(start)
		wg.Wait()

		won := -1
		for i, err := range errs {
			switch {
			case err == nil:
				if won >= 0 {
					t.Fatalf("round %d: both stored", round)
				}
				won = i
			case !errors.Is(err, ErrLanguageChanged):
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if won < 0 || st.Language() != langs[won] {
			t.Fatalf("round %d: winner %d, stored %q", round, won, st.Language())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		db.Close()
	}
}

// NoteUILanguage keeps the language of the app that last registered for
// pushes: only one this build carries, normalized, written only when it
// changed.
func TestNoteUILanguage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := &Settings{dao: dao.NewWithDB(db)}

	for _, skip := range []string{"", "xx", "x-klingon-tlh", "<script>", "en GB"} {
		if changed, err := st.NoteUILanguage(skip); changed || err != nil {
			t.Errorf("%q: %v %v", skip, changed, err)
		}
	}
	mock.ExpectExec("update `settings` set `last_ui_language` = \\?").WithArgs("en").WillReturnResult(sqlmock.NewResult(0, 1))
	if changed, err := st.NoteUILanguage("EN-gb"); !changed || err != nil || st.LastUILanguage() != "en" {
		t.Errorf("first: %v %v %q", changed, err, st.LastUILanguage())
	}
	// The same again (every launch re-registers): no write.
	if changed, err := st.NoteUILanguage("en"); changed || err != nil {
		t.Errorf("again: %v %v", changed, err)
	}
	if st.PushLanguage() != "en" {
		t.Errorf("push language %q", st.PushLanguage())
	}

	// Without the column: kept in memory only.
	st.lastUILanguage = ""
	mock.ExpectExec("update `settings` set `last_ui_language` = \\?").WillReturnError(&mysql.MySQLError{Number: 1054})
	if changed, err := st.NoteUILanguage("en"); !changed || err != nil || st.LastUILanguage() != "en" {
		t.Errorf("older schema: %v %v %q", changed, err, st.LastUILanguage())
	}
	st.lastUILanguage = ""
	mock.ExpectExec("update `settings` set `last_ui_language` = \\?").WillReturnError(errors.New("connection lost"))
	if changed, err := st.NoteUILanguage("en"); changed || err == nil || st.LastUILanguage() != "" {
		t.Errorf("failed write: %v %v %q", changed, err, st.LastUILanguage())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The push language: the stored choice, else the last app's, else English.
func TestPushLanguage(t *testing.T) {
	for _, c := range []struct{ stored, lastUI, want string }{
		{"", "", "en"},
		{"", "en", "en"},
		{"es", "en", "es"},
		{"ja", "en", "ja"},
		{"fr", "", "fr"},
	} {
		st := &Settings{language: c.stored, lastUILanguage: c.lastUI}
		if got := st.PushLanguage(); got != c.want {
			t.Errorf("%q, %q: got %q, want %q", c.stored, c.lastUI, got, c.want)
		}
	}
}
