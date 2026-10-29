// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"math"

	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

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
