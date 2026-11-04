// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A share link's archive that isn't there - an id unknown or swept, one
// the owner deleted, its file gone, a secret cut short - is one translated
// sentence for its visitor, as a gallery's is, never the raw cause ("sql:
// no rows in result set", a cipher's error); the device's own words stay
// its words, and a failing disk is "internal error".
func TestSharedLinkFailures(t *testing.T) {
	for _, lang := range []string{"", "es"} {
		notThere := devtext.Text(lang, filesmanager.ErrNoSuchGallery)
		if lang == "" && notThere != "this link doesn't exist or has expired" {
			t.Fatalf("the sentence is %q", notThere)
		}
		for name, err := range map[string]error{
			"unknown id":     errors.New("sql: no rows in result set"),
			"swept":          &fs.PathError{Op: "open", Path: "/mnt/storage/abc", Err: fs.ErrNotExist},
			"wrong secret":   errors.New("segcrypt: segment 0: cipher: message authentication failed"),
			"not an archive": errors.New("opening abc: segcrypt: not a segmented encrypted file"),
		} {
			if got := sharedLinkFailure(lang, err); got != notThere {
				t.Errorf("%s in %q: %q", name, lang, got)
			}
		}
		expired := devtext.New(i18n.DevFilesLinkExpired())
		if got, want := sharedLinkFailure(lang, expired), i18n.DevFilesDownloadFailed(devtext.Text(lang, expired)).Render(lang); got != want {
			t.Errorf("expired in %q: %q, want %q", lang, got, want)
		}
		disk := &fs.PathError{Op: "read", Path: "/mnt/storage/abc", Err: syscall.EIO}
		if got, want := sharedLinkFailure(lang, disk), i18n.DevFilesDownloadFailed(i18n.DevErrInternal().Render(lang)).Render(lang); got != want {
			t.Errorf("a failing disk in %q: %q, want %q", lang, got, want)
		}
	}
}

// Through the request: a mistyped link and one the sweep removed.
func TestDownloadingASharedLinkThatIsntThere(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)
	ch := &connHandler{mg: &Manager{dao: d, filesManager: filesmanager.NewWithDAO(d)}}
	download := func(uuid string) *pb.RespEnvelope {
		resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 1, Lang: "fr", Payload: &pb.ReqEnvelope_ReqDownloadSharedLink{
			ReqDownloadSharedLink: &pb.DownloadSharedLink{Uuid: uuid, Secret: "s", Length: 4 << 20}}})
		return resp
	}
	want := devtext.Text("fr", filesmanager.ErrNoSuchGallery)
	if resp := download("not-a-uuid"); !resp.Error || resp.ErrorMessage != want {
		t.Errorf("mistyped: %+v", resp)
	}
	mock.ExpectQuery("select `created`, `expires` from `shared_links`").WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}))
	if resp := download("0b9d7e6a-1c2f-4e3a-9b8c-7d6e5f4a3b2c"); !resp.Error || resp.ErrorMessage != want {
		t.Errorf("swept: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
