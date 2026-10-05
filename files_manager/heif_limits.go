// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"fmt"

	"github.com/jdeng/goheif"
	"github.com/jdeng/goheif/dav1d"
	"github.com/jdeng/goheif/heif"
	"github.com/jdeng/goheif/libde265"
)

// checkHeifGrid refuses a HEIC whose grid decodes to more than
// cMaxImagePixels, whatever its header says. checkImageSize goes by the
// image's 'ispe' property, but goheif sizes a grid's canvas from its first
// tile times the grid (up to 256x256 tiles, and the list may name the same
// tile again and again): a ~100 KB file saying 100x100 made it allocate
// 25 GB - a fatal out-of-memory no recover() catches, again at every
// sign-in as the analysis is resumed. The first tile is decoded here as
// goheif does it (goheif refuses tiles of another size), and that size,
// not the file's word, is what is checked. Anything that isn't a well
// formed grid is left to goheif, which fails on it as before.
func checkHeifGrid(b []byte) error {
	hf := heif.Open(bytes.NewReader(b))
	it, err := hf.PrimaryItem()
	if err != nil || it.Info == nil || it.Info.ItemType != "grid" {
		return nil
	}
	data, err := hf.GetItemData(it)
	if err != nil || len(data) < 8 {
		return nil
	}
	rows, cols := int(data[2])+1, int(data[3])+1
	dimg := it.Reference("dimg")
	if dimg == nil || len(dimg.ToItemIDs) != rows*cols {
		return nil
	}
	tw, th, ok := heifTileSize(hf, dimg.ToItemIDs[0])
	if !ok {
		return nil
	}
	w, h := int64(tw)*int64(cols), int64(th)*int64(rows)
	if w*h > cMaxImagePixels {
		return fmt.Errorf("the image is %dx%d, %w (over %d megapixels)", w, h, errImageTooLarge, cMaxImagePixels/1_000_000)
	}
	// goheif crops the canvas to these: larger, and the image's bounds
	// would run past its pixels.
	if sw, sh, ok := it.SpatialExtents(); ok && (int64(sw) > w || int64(sh) > h) {
		return fmt.Errorf("the image says it is %dx%d, larger than its %dx%d grid", sw, sh, w, h)
	}
	return nil
}

// heifTileSize decodes the grid tile id as goheif.Decode does, for its
// size.
func heifTileSize(hf *heif.File, id uint32) (w, h int, ok bool) {
	tile, err := hf.ItemByID(id)
	if err != nil || tile.Info == nil {
		return 0, 0, false
	}
	data, err := hf.GetItemData(tile)
	if err != nil {
		return 0, 0, false
	}
	switch tile.Info.ItemType {
	case "hvc1":
		hvcc, ok := tile.HevcConfig()
		if !ok {
			return 0, 0, false
		}
		dec, err := libde265.NewDecoder(libde265.WithSafeEncoding(goheif.SafeEncoding))
		if err != nil {
			return 0, 0, false
		}
		defer dec.Free()
		dec.Reset()
		dec.Push(hvcc.AsHeader())
		img, err := dec.DecodeImage(data)
		if err != nil {
			return 0, 0, false
		}
		return img.Bounds().Dx(), img.Bounds().Dy(), true
	case "av01":
		dec, err := dav1d.NewDecoder(dav1d.WithSafeEncoding(goheif.SafeEncoding))
		if err != nil {
			return 0, 0, false
		}
		defer dec.Free()
		dec.Reset()
		img, err := dec.DecodeImage(data)
		if err != nil {
			return 0, 0, false
		}
		return img.Bounds().Dx(), img.Bounds().Dy(), true
	}
	return 0, 0, false
}
