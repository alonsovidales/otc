// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"math"

	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// cCodePlacesOff is RespEnvelope.error_code on GetPhotoPlaces while Places
// is off: the apps hide the map.
const cCodePlacesOff = "places_off"

// placesArea is mediaArea, and while Places is off an area nothing is in:
// the device keeps no positions then (and turning it off deleted them).
func (ch *connHandler) placesArea(a *pb.PlaceArea) *dao.MediaArea {
	if a != nil && !ch.mg.filesManager.PlacesEnabled() {
		return &dao.MediaArea{South: 1, North: -1}
	}
	return mediaArea(a)
}

// mediaArea is SearchPhotos.area (and ReqPhotoDateBuckets.area) as the
// search takes it: nil for none, the whole library. A bound that isn't a
// number makes an area nothing is in (south above north), never the whole
// library.
func mediaArea(a *pb.PlaceArea) *dao.MediaArea {
	if a == nil {
		return nil
	}
	for _, v := range []float64{a.South, a.West, a.North, a.East} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return &dao.MediaArea{South: 1, North: -1}
		}
	}
	return &dao.MediaArea{South: a.South, West: a.West, North: a.North, East: a.East}
}
