// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"
	_ "image/png"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/alonsovidales/otc/log"
)

// Profile pictures: the owner's, and each friend's as their device sends
// it (a friend request, the friend sync's GetProfile). Every app has sent
// a crop of at most 45 KB since 2026-09-29, and the setup wizard one of at
// most 96 KB; before, the photo went as picked, several MB. A picture is
// stored with the friendship and sent again with every Alerts row and
// every feed post of its person, so one of 7.5 MB, which anyone with a
// device could send in a friend request again and again, made opening
// Alerts read and send hundreds of MB. A picture is kept within
// dao.MaxProfilePictureBytes: as it is when it fits, else made again small
// - or dropped (no picture) - depending on whose it is.

// PictureFrom says whose a profile picture is, which decides whether the
// device decodes it to make it small.
type PictureFrom int

const (
	// PictureFromStranger: a friend request's, before the owner accepted
	// it. Never decoded: anyone with a device can send one, as often as
	// they like. One over the cap is dropped; the friend sync fetches the
	// picture again once the request is accepted.
	PictureFromStranger PictureFrom = iota
	// PictureFromFriend: a friend's (the friend sync) or that of a device
	// the owner asked to be friends with. Decoded only as a JPEG or a PNG,
	// by Go's own decoders, within the decode budget.
	PictureFromFriend
	// PictureFromOwner: the owner's own, in any format the device shows
	// (HEIC from a phone included), turned as its metadata says.
	PictureFromOwner
)

// cProfilePictureSide is the longest side of a picture made again small: a
// few times what any app shows it at (a circle of 96 points at most).
const cProfilePictureSide = 512

// errPictureTooLarge: even at the lowest quality tried, the picture made
// again is over the cap.
var errPictureTooLarge = errors.New("the picture is too large even made small") // i18n-ignore: logged at Debug only

// FitProfilePicture is b as a profile picture is kept: b itself when it is
// a picture (a header Go reads: JPEG, PNG, GIF, WebP, HEIC) of at most
// dao.MaxProfilePictureBytes; over that, when from lets it be decoded, a
// JPEG of it at most cProfilePictureSide pixels a side within the cap; nil
// for no picture - empty, not a picture, or over the cap and not decoded.
func FitProfilePicture(b []byte, from PictureFrom) (out []byte) {
	if len(b) == 0 {
		return nil
	}
	// A header parser's panic on a stranger's bytes is no picture.
	defer func() {
		if r := recover(); r != nil {
			log.Debug("a profile picture's header could not be read:", r)
			out = nil
		}
	}()
	c, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || c.Width <= 0 || c.Height <= 0 {
		return nil
	}
	if len(b) <= dao.MaxProfilePictureBytes {
		return b
	}
	switch from {
	case PictureFromStranger:
		return nil
	case PictureFromFriend:
		if format != "jpeg" && format != "png" {
			return nil
		}
	}
	small, err := shrinkProfilePicture(b, format)
	if err != nil {
		log.Debug("a profile picture of", len(b), "bytes could not be made small:", err)
		return nil
	}
	return small
}

// shrinkProfilePicture decodes b (decodeImage: its header checked, the
// decode budget held, a HEIC through decodeHeic), turns it as its metadata
// says, scales it so its longest side is at most cProfilePictureSide and
// encodes it as a JPEG within the cap, lowering the quality as needed.
func shrinkProfilePicture(b []byte, format string) ([]byte, error) {
	img, err := decodeImage(b)
	if err != nil {
		return nil, err
	}
	switch format {
	case "jpeg":
		if ex, err := exifinfo.FromJPEG(b); err == nil && ex != nil {
			img = applyOrientation(img, ex.Orientation)
		}
	case "heic":
		orientation := 1
		if ex, err := exifinfo.FromHEIC(b); err == nil && ex != nil {
			orientation = ex.Orientation
		}
		img = applyHeicOrientation(b, img, orientation)
	}
	src := thumbnailSource(img, cProfilePictureSide)
	for q := 85; q >= 45; q -= 10 {
		out, err := encodeJPEG(src, q)
		if err != nil {
			return nil, err
		}
		if len(out) <= dao.MaxProfilePictureBytes {
			return out, nil
		}
	}
	return nil, errPictureTooLarge
}
