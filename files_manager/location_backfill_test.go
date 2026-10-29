// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/go-sql-driver/mysql"
)

// near matches a float argument within a millionth of a degree.
type near float64

func (n near) Match(v driver.Value) bool {
	f, ok := v.(float64)
	return ok && math.Abs(f-float64(n)) < 1e-6
}

// placesOn turns Places on in mg as the device read it, without asking
// the database.
func placesOn(mg *Manager) *Manager {
	mg.placesSwitch.on.Store(true)
	mg.placesSwitch.loaded.Store(true)
	return mg
}

func expectLocation(mock sqlmock.Sqlmock, hash string, lat, lon any) {
	mock.ExpectExec("insert into `photo_locations` \\(`hash`, `lat`, `lon`\\) values \\(\\?, \\?, \\?\\) on duplicate key update").
		WithArgs(hash, lat, lon).WillReturnResult(sqlmock.NewResult(1, 1))
}

// Places' backfill reads the metadata of what has a thumbnail and no row
// yet: a photo with GPS gets its position, one without a NULL row (read,
// never read again); content without a thumbnail, or whose blob is gone,
// is left to the processing that brings it; a video that couldn't be read
// (no stream) leaves the pass to run again at the next start, so the flag
// stays unset.
func TestBackfillLocationsReadsWhatHasNoRow(t *testing.T) {
	_, ses := galleryTestEnv(t)
	gps, err := os.ReadFile("../exifinfo/testdata/gps_test.jpg")
	if err != nil {
		t.Fatal(err)
	}
	want, err := exifinfo.FromJPEG(gps)
	if err != nil || !want.HasGPS {
		t.Fatalf("fixture %v, %v", want, err)
	}
	var plain bytes.Buffer
	jpeg.Encode(&plain, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil)

	h := func(i int) string { return fmt.Sprintf("%064x", 0x9100+i) }
	located, noGPS, noThumb, gone, video := h(1), h(2), h(3), h(4), h(5)
	for hash, content := range map[string][]byte{located: gps, noGPS: plain.Bytes(), noThumb: gps} {
		if err := blobstore.WriteBytes(blobPath(hash), ses, content); err != nil {
			t.Fatal(err)
		}
	}
	for _, hash := range []string{located, noGPS, gone, video} {
		if err := os.WriteFile(thumbnailPath(hash), []byte("thumb"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, hash := range []string{located, noGPS, noThumb, gone, video} {
			os.Remove(blobPath(hash))
			os.Remove(thumbnailPath(hash))
		}
	})

	db, mock, _ := sqlmock.New()
	t.Cleanup(func() { db.Close() })
	mg := placesOn(&Manager{dao: dao.NewWithDB(db)})
	mock.ExpectQuery("select `locations_backfilled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"d"}).AddRow(0))
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
	for _, r := range []struct{ hash, mime, path string }{
		{located, "image/jpeg", "/p/tuscany.jpg"}, {noGPS, "image/jpeg", "/p/plain.jpg"},
		{noThumb, "image/jpeg", "/p/new.jpg"}, {gone, "image/jpeg", "/p/gone.jpg"}, {video, "video/mp4", "/p/clip.mp4"},
	} {
		rows.AddRow(r.hash, r.mime, at, at, r.path, 10)
	}
	mock.ExpectQuery("from `files` as `f` left join `photo_locations` as `l` on `l`.`hash` = `f`.`hash` where `l`.`hash` is null").
		WithArgs("", cLocationBackfillPage).WillReturnRows(rows)
	expectLocation(mock, located, near(want.Latitude), near(want.Longitude))
	expectLocation(mock, noGPS, nil, nil)
	// Expected last, and never met: the video is to be read again.
	mock.ExpectExec("update `settings` set `locations_backfilled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))

	gen := mg.places.gen.Load()
	mg.BackfillLocations(ses)
	if err := mock.ExpectationsWereMet(); err == nil || !strings.Contains(err.Error(), "locations_backfilled") {
		t.Errorf("want only the flag unmet, got %v", err)
	}
	if mg.places.gen.Load() == gen {
		t.Error("positions written without telling the map")
	}

	// Once read to the end, it records so.
	db2, mock2, _ := sqlmock.New()
	t.Cleanup(func() { db2.Close() })
	mg = placesOn(&Manager{dao: dao.NewWithDB(db2)})
	mock2.ExpectQuery("select `locations_backfilled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"d"}).AddRow(0))
	mock2.ExpectQuery("left join `photo_locations`").WithArgs("", cLocationBackfillPage).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow(noGPS, "image/jpeg", at, at, "/p/plain.jpg", 10))
	expectLocation(mock2, noGPS, nil, nil)
	mock2.ExpectExec("update `settings` set `locations_backfilled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))
	mg.BackfillLocations(ses)
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Done once, or before the release's migration, it reads nothing.
func TestBackfillLocationsOnlyOnce(t *testing.T) {
	_, ses := galleryTestEnv(t)
	for _, c := range []struct {
		name string
		rows *sqlmock.Rows
		err  error
	}{
		{"done", sqlmock.NewRows([]string{"d"}).AddRow(1), nil},
		{"no column", nil, &mysql.MySQLError{Number: 1054, Message: "Unknown column 'locations_backfilled'"}},
	} {
		db, mock, _ := sqlmock.New()
		q := mock.ExpectQuery("select `locations_backfilled` from `settings`")
		if c.err != nil {
			q.WillReturnError(c.err)
		} else {
			q.WillReturnRows(c.rows)
		}
		placesOn(&Manager{dao: dao.NewWithDB(db)}).BackfillLocations(ses)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		db.Close()
	}
}

// Processing records the position it read, or that there is none, and
// tells the map; before the migration it records nothing, quietly.
func TestStoreLocation(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := placesOn(&Manager{dao: dao.NewWithDB(db)})
	hash := fmt.Sprintf("%064x", 0x9200)
	expectLocation(mock, hash, 43.5, 11.9)
	expectLocation(mock, hash, nil, nil)
	expectLocation(mock, hash, nil, nil) // a position off the globe is none
	mock.ExpectExec("insert into `photo_locations`").WillReturnError(&mysql.MySQLError{Number: 1146, Message: "Table 'photo_locations' doesn't exist"})
	gen := mg.places.gen.Load()
	mg.storeLocation(hash, &exifinfo.Info{HasGPS: true, Latitude: 43.5, Longitude: 11.9})
	mg.storeLocation(hash, nil)
	mg.storeLocation(hash, &exifinfo.Info{HasGPS: true, Latitude: 95, Longitude: 11.9})
	mg.storeLocation(hash, &exifinfo.Info{HasGPS: true, Latitude: 1, Longitude: 1})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if got := mg.places.gen.Load() - gen; got != 3 {
		t.Errorf("the map told %d times, want 3 (not for the write that failed)", got)
	}
}
