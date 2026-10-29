// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Places: GetPhotoPlaces answers PhotoPlaces, and the area of SearchPhotos
// and ReqPhotoDateBuckets reaches their query as the photo_locations join.
// In a child process, as TestSearchPhotosPassesItsThumbnailFlags: a search
// page loads a config.
func TestPlacesRequests(t *testing.T) {
	name := "TestPlacesRequests"
	if os.Getenv(cSearchChildEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), cSearchChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("in its own process: %v\n%s", err, out)
		}
		return
	}
	searchPhotosStorage(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2024, 6, 1, 8, 0, 0, 0, time.UTC)
	hash := fmt.Sprintf("%064x", 0x91ace)
	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	mock.ExpectQuery("from `files` as `f` join `photo_locations` as `l`").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "created", "video", "lat", "lon"}).AddRow(hash, at, 0, 43.7731, 11.256))
	mock.ExpectQuery("from `files` where `hash` in").WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow(hash, "image/jpeg", at, at, "/p/ponte.jpg", 1))
	// SearchPhotos and the buckets with Florence's area: south, north,
	// west, east in the join.
	mock.ExpectQuery("join `photo_locations` as `pl` on `pl`.`hash` = `f`.`hash` and `pl`.`lat` between \\? and \\? and `pl`.`lon` between \\? and \\?").
		WithArgs(43.7, 43.8, 11.2, 11.3).WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}))
	mock.ExpectQuery("join `photo_locations` as `pl` .* group by `bucket`").
		WithArgs(43.7, 43.8, 11.2, 11.3).WillReturnRows(sqlmock.NewRows([]string{"bucket", "n"}).AddRow("2024-06", 1))

	ch := &connHandler{mg: &Manager{filesManager: filesmanager.NewWithDAO(dao.NewWithDB(db))}}
	ch.setSession(newTestAuthenticatedSession(t))
	// Florence's tile at zoom 20, asked at 25: the device groups at 20.
	n := math.Exp2(20)
	r := 43.7731 * math.Pi / 180
	tile := &pb.PlaceTile{X: int32((11.256 + 180) / 360 * n), Y: int32((1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n)}
	resp, closeConn := ch.processMessage(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqGetPhotoPlaces{ReqGetPhotoPlaces: &pb.GetPhotoPlaces{
		Zoom: 25, Tiles: []*pb.PlaceTile{tile},
	}}})
	if closeConn || resp.GetError() {
		t.Fatalf("GetPhotoPlaces: %+v", resp)
	}
	places := resp.GetRespPhotoPlaces()
	if places.GetZoom() != 20 || places.GetLocatedTotal() != 1 || places.GetLocatedArea().GetNorth() != 43.7731 ||
		len(places.GetPlaces()) != 1 || places.Places[0].GetCover().GetPath() != "/p/ponte.jpg" || places.Places[0].X != tile.X {
		t.Errorf("answer %+v, want zoom 20 (25 clamped) and Florence's one photo", places)
	}

	area := &pb.PlaceArea{South: 43.7, West: 11.2, North: 43.8, East: 11.3}
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 4, Payload: &pb.ReqEnvelope_ReqSearchPhotos{ReqSearchPhotos: &pb.SearchPhotos{Area: area}}})
	if resp.GetError() || resp.GetRespListOfFiles() == nil {
		t.Errorf("SearchPhotos with an area: %+v", resp)
	}
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 5, Payload: &pb.ReqEnvelope_ReqPhotoDateBuckets{ReqPhotoDateBuckets: &pb.ReqPhotoDateBuckets{Area: area}}})
	if b := resp.GetRespPhotoDateBuckets().GetBuckets(); len(b) != 1 || b[0].Count != 1 {
		t.Errorf("buckets with an area: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// An area whose bounds aren't numbers holds nothing; no area is the whole
// library.
func TestMediaArea(t *testing.T) {
	if mediaArea(nil) != nil {
		t.Error("no area isn't the whole library")
	}
	if a := mediaArea(&pb.PlaceArea{South: math.NaN(), North: 10, East: 5}); a == nil || a.South <= a.North {
		t.Errorf("NaN area %+v holds something", a)
	}
	if a := mediaArea(&pb.PlaceArea{South: 1, West: 170, North: 2, East: -170}); *a != (dao.MediaArea{South: 1, West: 170, North: 2, East: -170}) {
		t.Errorf("area %+v", a)
	}
}
