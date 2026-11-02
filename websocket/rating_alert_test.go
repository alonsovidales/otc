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

// A Rating alert (release 163) is listed with its type and collection, and
// its title - in the request's language - is its actor_name too: all an
// app from before the type shows of it.
func TestListRatingNotification(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db), social: social.Init(dao.NewWithDB(db), nil, nil, nil, nil)}}
	ch.setSession(newTestAuthenticatedSession(t))

	alert := alerttext.New(i18n.DevAlertRatingNew(), i18n.DevAlertRatingNewDetail("Wedding"))
	lines, _ := alerttext.Lines(alert.DetailLine())
	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows(notificationColumns).
			AddRow("n1", time.Now(), "Rating", alert.Title, "", nil, nil, false, nil, nil, alert.Title, alert.Detail, 1, "g1")
	}
	mock.ExpectQuery(listNotifications + ".*n\\.group_id").WillReturnRows(rows())
	n := listNotificationsAs(t, "en", ch)
	if n.Type != pb.NotificationType_NotificationRating || n.GroupId != "g1" || n.ActorName != "Photos rated through a shared link" || n.Title != n.ActorName || n.Details != alert.Detail {
		t.Errorf("English: %+v", n)
	}

	mock.ExpectQuery(listNotifications).WillReturnRows(rows())
	mock.ExpectQuery("select `uuid`, `msg_key`, `msg_args`, `msg_lines` from `notifications`").WithArgs("n1").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "msg_key", "msg_args", "msg_lines"}).AddRow("n1", alert.Key, string(alert.Args), string(lines)))
	n = listNotificationsAs(t, "es", ch)
	if want := i18n.DevAlertRatingNew().Render("es"); n.Title != want || n.ActorName != want || n.GroupId != "g1" {
		t.Errorf("Spanish: %q %q %q, want %q", n.Title, n.ActorName, n.GroupId, want)
	}
	if want := i18n.DevAlertRatingNewDetail("Wedding").Render("es"); n.Details != want {
		t.Errorf("Spanish details %q, want %q", n.Details, want)
	}

	// Counted unread like any alert.
	mock.ExpectQuery("select count\\(\\*\\) from `notifications` where `acknowledged` = 0").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 8, Payload: &pb.ReqEnvelope_ReqGetNotificationCount{ReqGetNotificationCount: &pb.ReqGetNotificationCount{}}})
	if resp.Error || resp.GetRespNotificationCount().GetUnacknowledgedCount() != 1 {
		t.Errorf("count: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
