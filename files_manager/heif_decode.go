// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"reflect"

	"github.com/jdeng/goheif"
	"github.com/jdeng/goheif/heif"
	"github.com/jdeng/goheif/libde265"
)

// errNotHevcStill is decodeHeic's answer for content it leaves to goheif:
// not a HEIF, or one whose primary item isn't an HEVC picture or a grid of
// them (an AVIF's AV1, which goheif decodes through dav1d).
var errNotHevcStill = errors.New("not an HEVC still") // i18n-ignore: a sentinel the callers catch, never shown

// decodeHeic decodes a HEIC as goheif.Decode does - the primary item, one
// HEVC picture or a grid of them, cropped to the size it says - but right
// at 10 bits a sample, which phones that save HDR photos use. libde265
// hands those over as two bytes a sample, and goheif read them as one: a
// picture came out as noise, and a grid's tiles were placed at x times
// their rows' bytes, twice their width, until the last row ran past the
// image and the processing panicked ("slice bounds out of range
// [5243904:5242880]", TestDecodeHeicGridAtTenBits). Every picture here is
// brought to 8 bits first, and tiles are placed by their width.
func decodeHeic(b []byte) (image.Image, error) {
	hf := heif.Open(bytes.NewReader(b))
	it, err := hf.PrimaryItem()
	if err != nil || it.Info == nil {
		return nil, errNotHevcStill
	}
	switch it.Info.ItemType {
	case "hvc1":
		dec, err := libde265.NewDecoder(libde265.WithSafeEncoding(goheif.SafeEncoding))
		if err != nil {
			return nil, err
		}
		defer dec.Free()
		return heicPicture(dec, hf, it)
	case "grid":
		return decodeHeicGrid(hf, it)
	}
	return nil, errNotHevcStill
}

// heicPicture decodes one HEVC item (a still, or a grid's tile) as
// goheif's decodeHevcItem does, at 8 bits a sample.
func heicPicture(dec *libde265.Decoder, hf *heif.File, it *heif.Item) (*image.YCbCr, error) {
	hvcc, ok := it.HevcConfig()
	if !ok {
		return nil, errors.New("no hvcC") // i18n-ignore: goheif's own words for it, which reach only the log
	}
	data, err := hf.GetItemData(it)
	if err != nil {
		return nil, err
	}
	dec.Reset()
	dec.Push(hvcc.AsHeader())
	img, err := dec.DecodeImage(data)
	if err != nil {
		return nil, err
	}
	ycc, ok := img.(*image.YCbCr)
	if !ok {
		return nil, errors.New("tile is not YCbCr") // i18n-ignore: goheif's own words for it, which reach only the log
	}
	return eightBit(ycc, heicBitDepth(it)), nil
}

// decodeHeicGrid decodes a grid item: its HEVC tiles, each placed by its width
// (goheif placed them by their rows' bytes), then cropped to the size the
// image says, as goheif does. checkImageSize has refused a grid smaller
// than that size, or too large to decode, before this is reached.
func decodeHeicGrid(hf *heif.File, it *heif.Item) (image.Image, error) {
	width, height, ok := it.SpatialExtents()
	if !ok {
		return nil, errors.New("no dimension") // i18n-ignore: goheif's own words for it, which reach only the log
	}
	data, err := hf.GetItemData(it)
	if err != nil {
		return nil, err
	}
	if len(data) < 8 {
		return nil, errors.New("grid box too short") // i18n-ignore: reaches only the log
	}
	rows, cols := int(data[2])+1, int(data[3])+1
	dimg := it.Reference("dimg")
	if dimg == nil || len(dimg.ToItemIDs) != rows*cols {
		return nil, errors.New("tiles number not matched") // i18n-ignore: goheif's own words for it, which reach only the log
	}

	var dec *libde265.Decoder
	var out *image.YCbCr
	var tw, th, cw, ch int
	for i, id := range dimg.ToItemIDs {
		tile, err := hf.ItemByID(id)
		if err != nil {
			return nil, err
		}
		if tile.Info == nil || tile.Info.ItemType != "hvc1" {
			return nil, errNotHevcStill // AV1 tiles: goheif's
		}
		if dec == nil {
			if dec, err = libde265.NewDecoder(libde265.WithSafeEncoding(goheif.SafeEncoding)); err != nil {
				return nil, err
			}
			defer dec.Free()
		}
		pic, err := heicPicture(dec, hf, tile)
		if err != nil {
			return nil, err
		}
		r := pic.Bounds()
		if out == nil {
			tw, th = r.Dx(), r.Dy()
			cw, ch = chromaSize(pic)
			if int64(tw)*int64(cols) < int64(width) || int64(th)*int64(rows) < int64(height) {
				return nil, errors.New("grid smaller than the image") // i18n-ignore: checkHeifGrid refuses it first, in the owner's language
			}
			out = image.NewYCbCr(image.Rect(0, 0, tw*cols, th*rows), pic.SubsampleRatio)
		}
		if r.Dx() != tw || r.Dy() != th || pic.SubsampleRatio != out.SubsampleRatio {
			return nil, errors.New("inconsistent tile dimensions") // i18n-ignore: goheif's own words for it, which reach only the log
		}
		x, y := i%cols, i/cols
		for j := 0; j < th; j++ {
			copy(out.Y[(y*th+j)*out.YStride+x*tw:][:tw], pic.Y[j*pic.YStride:][:tw])
		}
		for j := 0; j < ch; j++ {
			at := (y*ch+j)*out.CStride + x*cw
			copy(out.Cb[at:][:cw], pic.Cb[j*pic.CStride:][:cw])
			copy(out.Cr[at:][:cw], pic.Cr[j*pic.CStride:][:cw])
		}
	}
	out.Rect = image.Rect(0, 0, width, height)
	return out, nil
}

// chromaSize is the width and height of a picture's Cb and Cr planes.
func chromaSize(p *image.YCbCr) (w, h int) {
	w, h = p.Rect.Dx(), p.Rect.Dy()
	switch p.SubsampleRatio {
	case image.YCbCrSubsampleRatio420:
		return (w + 1) / 2, (h + 1) / 2
	case image.YCbCrSubsampleRatio422:
		return (w + 1) / 2, h
	}
	return w, h
}

// eightBit is p at 8 bits a sample. libde265 gives a picture over 8 bits
// as two bytes a sample (little-endian, as both the Pi and a PC are), so
// its rows are at least twice as many bytes as it is wide - an 8-bit
// one's at most 15 bytes over its width, rounded up to 16 - and the bit
// depth says how far to shift.
func eightBit(p *image.YCbCr, depth int) *image.YCbCr {
	w, h := p.Rect.Dx(), p.Rect.Dy()
	if w < 16 || p.YStride < 2*w {
		return p
	}
	shift := depth - 8
	if shift <= 0 {
		shift = 2 // two bytes a sample with no depth to go by: 10 bits
	}
	cw, ch := chromaSize(p)
	out := &image.YCbCr{
		Y: make([]byte, w*h), Cb: make([]byte, cw*ch), Cr: make([]byte, cw*ch),
		YStride: w, CStride: cw, SubsampleRatio: p.SubsampleRatio, Rect: image.Rect(0, 0, w, h),
	}
	narrow := func(dst, src []byte, dstStride, srcStride, w, h int) {
		for y := 0; y < h; y++ {
			row := src[y*srcStride:]
			for x := 0; x < w; x++ {
				v := binary.LittleEndian.Uint16(row[2*x:]) >> shift
				if v > 255 {
					v = 255
				}
				dst[y*dstStride+x] = byte(v)
			}
		}
	}
	narrow(out.Y, p.Y, w, p.YStride, w, h)
	narrow(out.Cb, p.Cb, cw, p.CStride, cw, ch)
	narrow(out.Cr, p.Cr, cw, p.CStride, cw, ch)
	return out
}

// heicBitDepth is the luma bit depth an HEVC item's hvcC says (8 to 15),
// 0 when it can't be read. goheif keeps the record unexported, so it is
// read by reflection - TestHeicBitDepth notices a goheif that moves it.
func heicBitDepth(it *heif.Item) int {
	c, ok := it.HevcConfig()
	if !ok || c == nil {
		return 0
	}
	v := reflect.ValueOf(c).Elem().FieldByName("config")
	if !v.IsValid() {
		return 0
	}
	if v = v.FieldByName("bitDepthLuma"); !v.IsValid() || v.Kind() != reflect.Uint8 {
		return 0
	}
	return int(v.Uint()&7) + 8 // the record's 3 bits of bitDepthLumaMinus8
}
