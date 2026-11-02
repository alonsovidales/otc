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
	"github.com/alonsovidales/otc/segcrypt"
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

// Issue #203: a library holds blobs written before release 164 (OTS1) and
// after (OTS2, whose header is 25 bytes longer). Each is whole at the size
// its own format seals the row's content to, and at no other - a blob is
// never taken for whole by the other format's size.
func TestHasHashesInAMixedLibrary(t *testing.T) {
	_, ses := galleryTestEnv(t)
	hash := func(i int) string { return fmt.Sprintf("%064x", 0x4b00+i) }
	const size = 3<<20 + 99
	newBlob, oldBlob, oldAtNewSize, newAtOldSize, notOurs := hash(0), hash(1), hash(2), hash(3), hash(4)
	if err := blobstore.WriteBytes(blobPath(newBlob), ses, make([]byte, size)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(blobPath(newBlob)) })
	sparseBlob(t, blobPath(oldBlob), size, segcrypt.OTS1)
	// The right magic at the other format's size, and no magic at all.
	sparseBlob(t, blobPath(oldAtNewSize), size, segcrypt.OTS1)
	os.Truncate(blobPath(oldAtNewSize), segcrypt.OTS2.EncryptedSize(size))
	sparseBlob(t, blobPath(newAtOldSize), size, segcrypt.OTS2)
	os.Truncate(blobPath(newAtOldSize), segcrypt.OTS1.EncryptedSize(size))
	sparseBlob(t, blobPath(notOurs), size, 0)

	all := []string{newBlob, oldBlob, oldAtNewSize, newAtOldSize, notOurs}
	rows := sqlmock.NewRows([]string{"hash", "size"})
	for _, h := range all {
		rows.AddRow(h, size)
	}
	db, mock, _ := sqlmock.New()
	t.Cleanup(func() { db.Close() })
	mock.ExpectQuery("select `hash`, `size` from `files`").WillReturnRows(rows)
	found, err := (&Manager{dao: dao.NewWithDB(db)}).HasHashes(all)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(found, []string{newBlob, oldBlob}) {
		t.Errorf("found %v, want the OTS2 and the OTS1 blob at their own sizes", found)
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
