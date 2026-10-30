// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"context"
	"database/sql/driver"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	imagesquality "github.com/alonsovidales/otc/images_quality"
	"github.com/alonsovidales/otc/modelserver"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
)

// fakeScorer scores every photo score and keeps what it was given.
type fakeScorer struct {
	score float32
	err   error
	got   *image.RGBA
	calls int
}

func (f *fakeScorer) AestheticResized(_ context.Context, img *image.RGBA) (float32, error) {
	f.got = img
	f.calls++
	return f.score, f.err
}

// sharpThumb is a 400x300 JPEG of hard-edged squares: sharp and well lit.
func sharpThumb(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			v := uint8(60)
			if (x/10+y/10)%2 == 0 {
				v = 200
			}
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCameraOf(t *testing.T) {
	for _, c := range []struct {
		ex   *exifinfo.Info
		want bool
	}{
		{nil, false},
		{&exifinfo.Info{}, false},
		{&exifinfo.Info{CameraMake: "  "}, false},
		{&exifinfo.Info{CameraMake: "Apple"}, true},
		{&exifinfo.Info{CameraModel: "Pixel 8"}, true},
	} {
		if got := cameraOf(c.ex); got == nil || *got != c.want {
			t.Errorf("%+v: %v", c.ex, got)
		}
	}
}

// A photo is measured from its thumbnail: the model gets the 224x224
// picture; without a model, or when it fails, the measures alone.
func TestQualityOf(t *testing.T) {
	img, err := jpeg.Decode(bytes.NewReader(sharpThumb(t)))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeScorer{score: 6.2}
	mg := &Manager{aesthetic: f}
	on := true
	q := mg.qualityOf(img, &on)
	if q.Aesthetic == nil || *q.Aesthetic != 6.2 || f.got == nil || f.got.Rect.Dx() != imagesquality.InputSize {
		t.Errorf("aesthetic %v, the model got %v", q.Aesthetic, f.got)
	}
	if q.Width != 400 || q.Height != 300 || q.Sharpness < cMemoryMinSharpness || q.Dark != 0 || q.Bright != 0 || q.Brightness < 100 || q.Brightness > 160 || q.Camera != &on {
		t.Errorf("measures %+v", q)
	}
	for _, mg := range []*Manager{{}, {aesthetic: &fakeScorer{err: modelserver.ErrNoAesthetic}}, {aesthetic: &fakeScorer{err: fmt.Errorf("busy")}}} {
		if q := mg.qualityOf(img, nil); q.Aesthetic != nil || q.Sharpness == 0 {
			t.Errorf("without a model: %+v", q)
		}
	}
}

// expectQuality is one SetPhotoQuality: hash, its aesthetic (nil: none)
// and camera (nil: unknown), the rest whatever was measured.
func expectQuality(mock sqlmock.Sqlmock, hash string, aesthetic, camera driver.Value) {
	a, c := any(aesthetic), any(camera)
	if aesthetic != nil {
		a = sqlmock.AnyArg()
	}
	mock.ExpectExec("insert into `photo_quality`").
		WithArgs(hash, a, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 400, 300, c, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

func memoriesOn(mg *Manager) *Manager {
	mg.memories.read, mg.memories.on = true, true
	none := []string{}
	mg.outOfImagesCache.Store(&none)
	return mg
}

// The backfill measures what the database lists - from each photo's
// thumbnail, with whether its metadata names a camera from the photo
// itself (unknown when the photo can't be read) - and leaves a photo
// without a thumbnail to the processing that makes one.
func TestBackfillQualityMeasuresWhatHasNoRow(t *testing.T) {
	_, ses := galleryTestEnv(t)
	gps, err := os.ReadFile("../exifinfo/testdata/gps_test.jpg")
	if err != nil {
		t.Fatal(err)
	}
	ex, _ := exifinfo.FromJPEG(gps)
	fixtureCamera := *cameraOf(ex)
	thumb := sharpThumb(t)
	h := func(i int) string { return fmt.Sprintf("%064x", 0x9600+i) }
	withBlob, noBlob, noThumb := h(1), h(2), h(3)
	if err := blobstore.WriteBytes(blobPath(withBlob), ses, gps); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{withBlob, noBlob} {
		if err := blobstore.WriteBytes(thumbnailPath(hash), ses, thumb); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, hash := range []string{withBlob, noBlob, noThumb} {
			os.Remove(blobPath(hash))
			os.Remove(thumbnailPath(hash))
		}
	})

	db, mock, _ := sqlmock.New()
	t.Cleanup(func() { db.Close() })
	f := &fakeScorer{score: 5.8}
	mg := memoriesOn(&Manager{dao: dao.NewWithDB(db), aesthetic: f})
	rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, hash := range []string{withBlob, noBlob, noThumb} {
		rows.AddRow(hash, "image/jpeg", at, at, "/p/"+hash[60:]+".jpg", 10)
	}
	// With the model: rows measured without it are listed too.
	mock.ExpectQuery("left join `photo_quality` as `q` on `q`.`hash` = `f`.`hash` where \\(`q`.`hash` is null or `q`.`aesthetic` is null\\)").
		WithArgs("", cQualityBackfillPage).WillReturnRows(rows)
	expectQuality(mock, withBlob, 5.8, fixtureCamera)
	expectQuality(mock, noBlob, 5.8, nil)

	gen := mg.memories.gen.Load()
	mg.BackfillQuality(ses)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if f.calls != 2 {
		t.Errorf("the model scored %d photos, want 2", f.calls)
	}
	if mg.memories.gen.Load() == gen {
		t.Error("measures written without dropping the answers kept")
	}
	if mg.memories.measuring.Load() {
		t.Error("still measuring after the end")
	}

	// Off, or before the migration, nothing.
	db2, mock2, _ := sqlmock.New()
	t.Cleanup(func() { db2.Close() })
	mg = &Manager{dao: dao.NewWithDB(db2)}
	mg.memories.read = true // off
	mg.BackfillQuality(ses)
	mg.storeQuality(ses, &pb.File{Hash: withBlob, Mime: "image/jpeg"}, nil)
	mock2.ExpectQuery("photo_quality").WillReturnError(&mysql.MySQLError{Number: 1146, Message: "Table 'photo_quality' doesn't exist"})
	mg = memoriesOn(&Manager{dao: dao.NewWithDB(db2)})
	mg.BackfillQuality(ses)
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Processing measures a photo it analyses from the thumbnail it wrote,
// with the camera its metadata names, and drops the answers kept.
func TestStoreQuality(t *testing.T) {
	_, ses := galleryTestEnv(t)
	hash := fmt.Sprintf("%064x", 0x9700)
	if err := blobstore.WriteBytes(thumbnailPath(hash), ses, sharpThumb(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(thumbnailPath(hash)) })
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := memoriesOn(&Manager{dao: dao.NewWithDB(db)})
	expectQuality(mock, hash, nil, true)
	gen := mg.memories.gen.Load()
	mg.storeQuality(ses, &pb.File{Hash: hash, Mime: "image/jpeg"}, &exifinfo.Info{CameraMake: "Canon"})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if mg.memories.gen.Load() == gen {
		t.Error("the answers kept weren't dropped")
	}
	// No thumbnail: nothing written, nothing said.
	mg.storeQuality(ses, &pb.File{Hash: fmt.Sprintf("%064x", 0x9701), Mime: "image/jpeg"}, nil)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
