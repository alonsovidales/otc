// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// The alert for ratings through a link (release 163) is a row of type
// Rating of its own: its keys stored as an alert's, the collection in
// group_id, the title as actor_name too.
func TestAddRatingNotification(t *testing.T) {
	d, mock := ratingsMock(t)
	a := alerttext.New(i18n.DevAlertRatingNew(), i18n.DevAlertRatingNewDetail("Wedding"))
	mock.ExpectExec("insert into `notifications` \\(`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`, `msg_key`, `msg_args`, `msg_lines`, `group_id`\\) values \\(\\?, now\\(\\), 'Rating', \\?, '', \\?, \\?, 1, \\?, \\?, \\?, \\?\\)").
		WithArgs(sqlmock.AnyArg(), a.Title, a.Title, a.Detail, "dev.alert.rating.new", "{}", sqlmock.AnyArg(), "g1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.AddRatingNotification("g1", a); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("insert into `notifications`").WillReturnError(unknownColumn("group_id"))
	if err := d.AddRatingNotification("g1", a); !errors.Is(err, ErrSchemaBehind) {
		t.Fatalf("schema behind: %v", err)
	}
	if got := d.strToNotificationType(d.notificationTypeToStr(pb.NotificationType_NotificationRating)); got != pb.NotificationType_NotificationRating {
		t.Errorf("the type doesn't read back: %v", got)
	}
}

// The listing carries a Rating row's collection and its title as
// actor_name; a database without the column is listed as before.
func TestListNotificationsRating(t *testing.T) {
	d, mock := ratingsMock(t)
	cols := []string{"uuid", "dt", "type", "actor_name", "actor_domain", "pub_uuid", "comment_uuid", "acknowledged", "image", "hash", "title", "details", "occurrences"}
	mock.ExpectQuery("n\\.occurrences, n\\.group_id from `notifications` n").WithArgs(MaxProfilePictureBytes, 10).
		WillReturnRows(sqlmock.NewRows(append(cols, "group_id")).
			AddRow("n1", time.Now(), "Rating", "", "", nil, nil, false, nil, nil, "Photos rated through a shared link", "d", 1, "g1").
			AddRow("n2", time.Now(), "Error", "This device", "", nil, nil, false, nil, nil, "t", "d", 1, nil))
	ns, _, err := d.ListNotifications(10)
	if err != nil || len(ns) != 2 {
		t.Fatalf("%v %v", ns, err)
	}
	if r := ns[0]; r.Type != pb.NotificationType_NotificationRating || r.GroupId != "g1" || r.ActorName != "Photos rated through a shared link" {
		t.Errorf("rating row %+v", r)
	}
	if e := ns[1]; e.Type != pb.NotificationType_NotificationError || e.GroupId != "" || e.ActorName != "This device" {
		t.Errorf("error row %+v", e)
	}

	mock.ExpectQuery("n\\.group_id from").WillReturnError(unknownColumn("n.group_id"))
	mock.ExpectQuery("n\\.occurrences from `notifications` n").WithArgs(MaxProfilePictureBytes, 10).
		WillReturnRows(sqlmock.NewRows(cols).AddRow("n2", time.Now(), "Error", "This device", "", nil, nil, false, nil, nil, "t", "d", 1))
	if ns, _, err := d.ListNotifications(10); err != nil || len(ns) != 1 || ns[0].Type != pb.NotificationType_NotificationError {
		t.Fatalf("schema behind: %v %v", ns, err)
	}

	// Rendered in another language, its actor_name follows its title.
	a := alerttext.New(i18n.DevAlertRatingNew(), i18n.DevAlertRatingNewDetail("Wedding"))
	lines, _ := alerttext.Lines(a.DetailLine())
	n := &pb.Notification{Uuid: "n1", Type: pb.NotificationType_NotificationRating, Title: a.Title, Details: a.Detail, ActorName: a.Title}
	mock.ExpectQuery("select `uuid`, `msg_key`, `msg_args`, `msg_lines` from `notifications`").WithArgs("n1").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "msg_key", "msg_args", "msg_lines"}).AddRow("n1", a.Key, string(a.Args), string(lines)))
	if err := d.LocalizeNotifications("fr", []*pb.Notification{n}); err != nil {
		t.Fatal(err)
	}
	if want := i18n.DevAlertRatingNew().Render("fr"); n.Title != want || n.ActorName != want {
		t.Errorf("French: %q %q, want %q", n.Title, n.ActorName, want)
	}
}
