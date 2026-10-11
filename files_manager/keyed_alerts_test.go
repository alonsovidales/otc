// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// SetOutOfImages' refusals say what they always said in English, whatever
// language a request without one, or in English, asks in.
func TestKeptOutRefusalsKeepTheirEnglish(t *testing.T) {
	for path, want := range map[string]string{
		"Photos": "the folder must be a full path",
		"/":      "the whole library can't be kept out of Images",
		"/" + strings.Repeat("a", cMaxFolderPath+1): "the folder's path is too long",
		"/a/\x00": "the folder's path is not valid",
		"/a/../b": "the folder's path is not valid",
	} {
		_, err := outOfImagesFolder(path)
		if err == nil || err.Error() != want || devtext.Text("", err) != want || devtext.Text("en", err) != want {
			t.Errorf("%.20q: %v", path, err)
		}
	}
	for _, c := range []struct {
		e    *OutOfImagesByParentError
		want string
	}{
		{&OutOfImagesByParentError{Folder: "/Photos/Trip/", Parent: "/Photos/"}, fmt.Sprintf("%s is inside %s, which is kept out of Images - show %s in Images to show %s", "Trip", "/Photos", "/Photos", "Trip")},
		{&OutOfImagesByParentError{Folder: "/Photos/Trip/", Parent: "/Photos/", OwnFlag: true}, fmt.Sprintf("%s is inside %s, which is kept out of Images - show %s in Images first, then %s", "Trip", "/Photos", "/Photos", "Trip")},
	} {
		var err error = c.e
		if err.Error() != c.want || devtext.Text("", err) != c.want || devtext.Text("en-GB", err) != c.want {
			t.Errorf("got %q / %q, want %q", err.Error(), devtext.Text("", err), c.want)
		}
	}
}

// A shared gallery's failure is kept with its key, and a job read in
// English gets the English the copy always reported.
func TestGalleryJobErrorsKeepTheirEnglish(t *testing.T) {
	for _, c := range []struct {
		m    i18n.Msg
		want string
	}{
		{i18n.DevGalleryjobCopyCrashed(), "the copy stopped: a file crashed the processing"},
		{i18n.DevGalleryjobNoSmallCopy("IMG_1.HEIC"), fmt.Sprintf("could not make a small copy of %s", "IMG_1.HEIC")},
		{i18n.DevGalleryjobCopyFailed("IMG_1.HEIC", "no space left on device"), fmt.Errorf("could not copy %s: %w", "IMG_1.HEIC", errors.New("no space left on device")).Error()},
	} {
		err := devtext.WithEnglish(alerttext.EnglishOf(c.m), c.m)
		job := &galleryJob{state: &pb.SharedGalleryJob{JobId: "j", Finished: true, Error: err.Error()}, err: err}
		for _, lang := range []string{"", "en"} {
			if got := job.snapshot(lang).Error; got != c.want {
				t.Errorf("%s in %q: %q, want %q", c.m.Key, lang, got, c.want)
			}
		}
	}
	// A job whose failure has no key of ours: the error as it is.
	err := errors.New("mkdir: permission denied")
	job := &galleryJob{state: &pb.SharedGalleryJob{JobId: "j", Finished: true, Error: err.Error()}, err: err}
	if got := job.snapshot("en").Error; got != "mkdir: permission denied" {
		t.Errorf("a raw error: %q", got)
	}
	if got := (&galleryJob{state: &pb.SharedGalleryJob{JobId: "j"}}).snapshot("en").Error; got != "" {
		t.Errorf("a running job: %q", got)
	}
}

// The memory Alert's month names follow time's ("2 Jan 15:04").
func TestMemoryAlertMonths(t *testing.T) {
	for m := 1; m <= 12; m++ {
		if got, want := alerttext.EnglishOf(monthNames[m-1]()), time.Month(m).String()[:3]; got != want {
			t.Errorf("month %d: %q, want %q", m, got, want)
		}
	}
}
