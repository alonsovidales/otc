// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/social"
)

const listNotifications = "select n\\.uuid, n\\.dt, n\\.type"

func notificationRows(title, details string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"uuid", "dt", "type", "actor_name", "actor_domain", "pub_uuid", "comment_uuid", "acknowledged", "image", "hash", "title", "details", "occurrences"}).
		AddRow("n1", time.Now(), "Error", "This device", "", nil, nil, false, nil, nil, title, details, 1)
}

func listNotificationsAs(t *testing.T, lang string, ch *connHandler) *pb.Notification {
	t.Helper()
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 7, Lang: lang, Payload: &pb.ReqEnvelope_ReqListNotifications{ReqListNotifications: &pb.ReqListNotifications{Limit: 10}}})
	if resp.Error {
		t.Fatalf("%q: %s", lang, resp.ErrorMessage)
	}
	ns := resp.GetRespNotifications().GetNotifications()
	if len(ns) != 1 {
		t.Fatalf("%q: %d notifications", lang, len(ns))
	}
	return ns[0]
}

// ListNotifications answers an alert in the request's language, from the
// keys stored with it - and a request without a language, or in English,
// gets the stored English as it always did, without another query.
func TestListNotificationsInTheRequestsLanguage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db), social: social.Init(dao.NewWithDB(db), nil, nil, nil, nil)}}
	ch.setSession(newTestAuthenticatedSession(t))

	alert := alerttext.WithRaw(i18n.DevAlertFileNotRead("IMG_1.jpg"), "EOF")
	at := time.Now()
	lines, _ := alerttext.Lines(alert.Line(at))
	english := alert.EnglishLine(at)
	for _, lang := range []string{"", "en", "en-GB"} {
		mock.ExpectQuery(listNotifications).WillReturnRows(notificationRows(alert.Title, english))
		if n := listNotificationsAs(t, lang, ch); n.Title != "IMG_1.jpg could not be read" || n.Details != english {
			t.Errorf("%q: %q | %q", lang, n.Title, n.Details)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	lang := ""
	for _, l := range i18n.Languages() {
		if l.Code == "qps" || (lang == "" && l.Code != "en") {
			lang = l.Code
		}
	}
	if lang == "" {
		t.Skip("this build carries only English")
	}
	mock.ExpectQuery(listNotifications).WillReturnRows(notificationRows(alert.Title, english))
	mock.ExpectQuery("select `uuid`, `msg_key`, `msg_args`, `msg_lines` from `notifications`").WithArgs("n1").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "msg_key", "msg_args", "msg_lines"}).AddRow("n1", alert.Key, string(alert.Args), string(lines)))
	n := listNotificationsAs(t, lang, ch)
	if want := i18n.DevAlertFileNotRead("IMG_1.jpg").Render(lang); n.Title != want {
		t.Errorf("%s title %q, want %q", lang, n.Title, want)
	}
	if want := i18n.DevAlertLineDetail(at.Format("15:04"), i18n.DevAlertFileNotRead("IMG_1.jpg").Render(lang), "EOF").Render(lang); n.Details != want {
		t.Errorf("%s details %q, want %q", lang, n.Details, want)
	}

	// The keys can't be read: the English, and still an answer.
	mock.ExpectQuery(listNotifications).WillReturnRows(notificationRows(alert.Title, english))
	mock.ExpectQuery("select `uuid`, `msg_key`").WillReturnError(sqlmock.ErrCancelled)
	if n := listNotificationsAs(t, lang, ch); n.Title != alert.Title || n.Details != english {
		t.Errorf("unreadable keys: %q | %q", n.Title, n.Details)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
