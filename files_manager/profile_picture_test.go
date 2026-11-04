// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"

	"github.com/alonsovidales/otc/dao"
)

// noisy is a w x h picture of noise: it doesn't compress, so it is large
// as a JPEG or a PNG at any size worth testing.
func noisy(w, h int) *image.RGBA {
	r := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(r.Intn(256))
	}
	return img
}

// withOrientation puts an EXIF Orientation tag in front of a JPEG's own
// segments (testJPEG's APP1).
func withOrientation(b []byte, orientation int) []byte {
	tiff := []byte{'I', 'I', 0x2a, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(orientation), 0, 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := append([]byte{0xff, 0xd8}, seg...)
	return append(out, b[2:]...)
}

func encoded(t *testing.T, img image.Image, format string) []byte {
	t.Helper()
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	case "png":
		err = png.Encode(&buf, img)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A profile picture within the cap is kept as it came, whoever's it is;
// one over it is made small - a JPEG within the cap, its longest side
// cProfilePictureSide - when its source may be decoded, else dropped; and
// what isn't a picture is never kept.
func TestFitProfilePicture(t *testing.T) {
	small := testJPEG(t, 96, 96, 0)
	bigJPEG := encoded(t, noisy(1600, 1200), "jpeg")
	bigPNG := encoded(t, noisy(700, 700), "png")
	bigGIF := encoded(t, noisy(900, 900), "gif")
	for _, b := range [][]byte{bigJPEG, bigPNG, bigGIF} {
		if len(b) <= dao.MaxProfilePictureBytes {
			t.Fatalf("a test picture of %d bytes is within the cap", len(b))
		}
	}
	every := []PictureFrom{PictureFromStranger, PictureFromFriend, PictureFromOwner}
	for _, from := range every {
		if got := FitProfilePicture(small, from); !bytes.Equal(got, small) {
			t.Errorf("%d: a small picture wasn't kept as it came", from)
		}
		for _, junk := range [][]byte{nil, {}, []byte("not a picture at all"), bigJPEG[:200]} {
			if got := FitProfilePicture(junk, from); got != nil {
				t.Errorf("%d: %d bytes that aren't a picture kept", from, len(junk))
			}
		}
	}

	madeSmall := func(name string, got []byte, w, h int) {
		t.Helper()
		if got == nil || len(got) > dao.MaxProfilePictureBytes {
			t.Fatalf("%s: %d bytes", name, len(got))
		}
		if gw, gh := jpegSize(t, got); gw != w || gh != h {
			t.Errorf("%s: %dx%d, want %dx%d", name, gw, gh, w, h)
		}
	}
	// A stranger's is never decoded.
	for _, b := range [][]byte{bigJPEG, bigPNG, bigGIF} {
		if FitProfilePicture(b, PictureFromStranger) != nil {
			t.Errorf("a stranger's %d bytes were kept", len(b))
		}
	}
	// A friend's JPEG or PNG is made small; other formats aren't decoded.
	madeSmall("friend's JPEG", FitProfilePicture(bigJPEG, PictureFromFriend), 512, 384)
	madeSmall("friend's PNG", FitProfilePicture(bigPNG, PictureFromFriend), 512, 512)
	if FitProfilePicture(bigGIF, PictureFromFriend) != nil {
		t.Error("a friend's large GIF was decoded")
	}
	// A friend's picture of more pixels than a phone's photo: not decoded
	// (the bound lowered under the test's 1600 x 1200).
	saved := cMaxFriendPicturePixels
	cMaxFriendPicturePixels = 1600*1200 - 1
	if FitProfilePicture(bigJPEG, PictureFromFriend) != nil {
		t.Error("a friend's picture over the pixel bound was decoded")
	}
	cMaxFriendPicturePixels = saved
	// The owner's own, any format, turned as its metadata says.
	madeSmall("owner's JPEG", FitProfilePicture(bigJPEG, PictureFromOwner), 512, 384)
	madeSmall("owner's GIF", FitProfilePicture(bigGIF, PictureFromOwner), 512, 512)
	madeSmall("owner's turned JPEG", FitProfilePicture(withOrientation(bigJPEG, 6), PictureFromOwner), 384, 512)
}
