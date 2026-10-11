// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"testing"

	"github.com/alonsovidales/otc/devtext"
)

// The errors a reply shows kept the English they had before they moved
// into the catalog: Error() (the log, an alert's detail) and the reply to
// a request without a language or in English.
func TestReplyErrorsKeepTheirEnglish(t *testing.T) {
	mg := &Manager{}
	_, chunkErr := mg.UploadChunk(nil, "", 0, make([]byte, MaxChunk+1))
	_, badSize := mg.BeginUpload(nil, "/a", -1, false, nil, nil, "", nil)
	_, noPath := mg.BeginUpload(nil, "", 1, false, nil, nil, "", nil)
	_, unknown := mg.FinishUpload(nil, "nope", "")
	cases := []struct {
		err  error
		want string
	}{
		{ErrUploadOnly, "this folder is upload only: nothing in it can be deleted"},
		{ErrDuplicatedFile, "Duplicated file"},
		{ErrUnknownUpload, "unknown or expired upload"},
		{ErrOutOfOrder, "upload chunk out of order"},
		{ErrNoSuchGallery, "this link doesn't exist or has expired"},
		{chunkErr, "chunk over 4194304 bytes"},
		{badSize, "bad size"},
		{noPath, "no path"},
		{unknown, "unknown or expired upload"},
		{imageTooLarge(12000, 11000), "the image is 12000x11000, too large to process (over 120 megapixels)"},
		{&LockedByParentError{Folder: "/Photos/Trip/", Parent: "/Photos/", OwnFlag: true}, "Trip is inside the upload-only folder /Photos - unlock /Photos first, then Trip"},
		{&LockedByParentError{Folder: "/Photos/Trip/", Parent: "/Photos/"}, "Trip is inside the upload-only folder /Photos - unlock /Photos to unlock it"},
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("want %q, got no error", c.want)
			continue
		}
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error(): got %q, want %q", got, c.want)
		}
		for _, lang := range []string{"", "en"} {
			if got := devtext.Text(lang, c.err); got != c.want {
				t.Errorf("reply in %q: got %q, want %q", lang, got, c.want)
			}
		}
	}
	if !errors.Is(imageTooLarge(12000, 11000), errImageTooLarge) {
		t.Error("imageTooLarge must still wrap errImageTooLarge")
	}
}
