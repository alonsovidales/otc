// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql/driver"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
)

// Free up space: a phone deletes its copy of what HasHashes answers, so
// only content whose blob is on the disk whole counts.
func TestHasHashesAnswersOnlyWholeContent(t *testing.T) {
	_, ses := galleryTestEnv(t)
	hash := func(i int) string { return fmt.Sprintf("%064x", 0x4a50+i) }
	content := []byte(strings.Repeat("photo", 1000))
	whole, cut, gone, noRow := hash(0), hash(1), hash(2), hash(3)
	for _, h := range []string{whole, cut} {
		if err := blobstore.WriteBytes(blobPath(h), ses, content); err != nil {
			t.Fatal(err)
		}
	}
	// A blob cut short: there, not empty, but not what its row seals to.
	if err := os.Truncate(blobPath(cut), 100); err != nil {
		t.Fatal(err)
	}
	os.Remove(blobPath(gone))
	t.Cleanup(func() { os.Remove(blobPath(whole)); os.Remove(blobPath(cut)) })

	db, mock, _ := sqlmock.New()
	t.Cleanup(func() { db.Close() })
	// Each hash asked once, the malformed ones never.
	mock.ExpectQuery("select `hash`, `size` from `files` where `hash` in \\(\\?,\\?,\\?,\\?\\)").
		WithArgs(whole, cut, gone, noRow).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "size"}).
			AddRow(whole, len(content)).AddRow(cut, len(content)).AddRow(gone, len(content)))
	mg := &Manager{dao: dao.NewWithDB(db)}

	found, err := mg.HasHashes([]string{whole, cut, "../" + whole, gone, strings.ToUpper(whole), noRow, whole})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(found, []string{whole}) {
		t.Errorf("found %v, want only the whole one", found)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The row's size is the plain content's: the same content stored again is
// still whole, and a row that claims another size isn't.
func TestHasHashesComparesTheSealedSize(t *testing.T) {
	_, ses := galleryTestEnv(t)
	h := fmt.Sprintf("%064x", 0x4a60)
	content := make([]byte, 3<<20+5) // four segments
	if err := blobstore.WriteBytes(blobPath(h), ses, content); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(blobPath(h)) })
	for _, tc := range []struct {
		size int64
		want bool
	}{{int64(len(content)), true}, {int64(len(content)) - 1, false}, {0, false}} {
		db, mock, _ := sqlmock.New()
		mock.ExpectQuery("select `hash`, `size` from `files`").
			WillReturnRows(sqlmock.NewRows([]string{"hash", "size"}).AddRow(h, tc.size))
		found, err := (&Manager{dao: dao.NewWithDB(db)}).HasHashes([]string{h})
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := len(found) == 1; got != tc.want {
			t.Errorf("row size %d: found %v, want %v", tc.size, found, tc.want)
		}
	}
}

// No query at all for nothing to look up, and never more than the cap.
func TestHasHashesCapsAndSkipsEmpty(t *testing.T) {
	db, mock, _ := sqlmock.New()
	t.Cleanup(func() { db.Close() })
	mg := &Manager{dao: dao.NewWithDB(db)}
	if found, err := mg.HasHashes([]string{"", "nothex"}); err != nil || len(found) != 0 {
		t.Fatalf("found %v, err %v", found, err)
	}
	var many []string
	for i := 0; i < cMaxHasHashes+10; i++ {
		many = append(many, fmt.Sprintf("%064x", 0x9000+i))
	}
	mock.ExpectQuery("select `hash`, `size` from `files`").
		WithArgs(func() []driver.Value {
			var a []driver.Value
			for _, h := range many[:cMaxHasHashes] {
				a = append(a, h)
			}
			return a
		}()...).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "size"}))
	if _, err := mg.HasHashes(many); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
