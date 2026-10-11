// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// captured is a sqlmock argument that matches anything and keeps it.
type captured struct{ v driver.Value }

func (c *captured) Match(v driver.Value) bool { c.v = v; return true }

func (c *captured) str() string {
	switch v := c.v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

func (c *captured) lines(t *testing.T) []alerttext.Line {
	t.Helper()
	var ls []alerttext.Line
	if err := json.Unmarshal([]byte(c.str()), &ls); err != nil {
		t.Fatalf("msg_lines %q: %v", c.str(), err)
	}
	return ls
}

const (
	keyedErrorInsert = "insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`, `msg_key`, `msg_args`, `msg_lines`) values (?, now(), 'Error', 'This device', ?, ?, ?, 1, ?, ?, ?)"
	plainErrorInsert = "insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`) values (?, now(), 'Error', 'This device', ?, ?, ?, 1)"
	groupAppend      = "update `notifications` set `details` = concat(coalesce(`details`, ''), '\n', ?), `occurrences` = `occurrences` + 1, `acknowledged` = 0"
)

func fileAlert(name, detail string) alerttext.Alert {
	return alerttext.WithRaw(i18n.DevAlertFileNotRead(name), detail)
}

// A new group is written in English, as before, and with its keys: the
// title's key and arguments, and its first line.
func TestAddErrorNotificationStoresItsKeys(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	details, args, lines := &captured{}, &captured{}, &captured{}
	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n", "msg_key", "msg_lines"}))
	mock.ExpectExec(regexp.QuoteMeta(keyedErrorInsert)).
		WithArgs(sqlmock.AnyArg(), "", "IMG_1.jpg could not be read", details, "dev.alert.file.not_read", args, lines).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	before := time.Now()
	if err := NewWithDB(db).AddErrorNotification(fileAlert("IMG_1.jpg", "EOF")); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	clock, rest, _ := strings.Cut(details.str(), " ")
	if _, err := time.Parse("15:04", clock); err != nil || rest != "IMG_1.jpg could not be read: EOF" {
		t.Errorf("details %q, want \"15:04 IMG_1.jpg could not be read: EOF\"", details.str())
	}
	if args.str() != `{"name":"IMG_1.jpg"}` {
		t.Errorf("msg_args %q", args.str())
	}
	ls := lines.lines(t)
	if len(ls) != 1 || ls[0].Key != "dev.alert.file.not_read" || string(ls[0].Args) != `{"name":"IMG_1.jpg"}` || ls[0].Detail != "EOF" {
		t.Fatalf("msg_lines %+v", ls)
	}
	if at, err := time.Parse(time.RFC3339, ls[0].At); err != nil || at.Before(before.Add(-time.Second)) || at.Location() != time.UTC {
		t.Errorf("line time %q (%v)", ls[0].At, err)
	}
}

// Joining a group: the line goes to details and to msg_lines; an error
// with only its English makes the row English whole; a row with only its
// English stays so, and so does msg_lines once it is full.
func TestAddErrorNotificationJoinsItsLines(t *testing.T) {
	first, _ := alerttext.Lines(fileAlert("a.jpg", "EOF").Line(time.Now()))
	full := first
	for i := 1; i < alerttext.MaxLines; i++ {
		full, _ = alerttext.AppendLine(full, fileAlert("a.jpg", "EOF").Line(time.Now()))
	}
	for _, c := range []struct {
		name     string
		key      any
		lines    any
		alert    alerttext.Alert
		expected string // the update's own part after groupAppend
		args     int
	}{
		{"keyed into keyed", "dev.alert.file.not_read", string(first), fileAlert("b.jpg", "EOF"), ", `msg_lines` = ? where `uuid` = ?", 3},
		{"English into keyed", "dev.alert.file.not_read", string(first), alerttext.English("b.jpg broke", "EOF"), ", `msg_key` = null, `msg_args` = null, `msg_lines` = null where `uuid` = ?", 2},
		{"keyed into English", nil, nil, fileAlert("b.jpg", "EOF"), " where `uuid` = ?", 2},
		{"keyed into a full msg_lines", "dev.alert.file.not_read", string(full), fileAlert("b.jpg", "EOF"), " where `uuid` = ?", 2},
	} {
		db, mock, _ := sqlmock.New()
		lines := &captured{}
		mock.ExpectBegin()
		mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n", "msg_key", "msg_lines"}).AddRow("g1", 100, c.key, c.lines))
		args := []driver.Value{sqlmock.AnyArg(), lines, "g1"}
		if c.args == 2 {
			args = []driver.Value{sqlmock.AnyArg(), "g1"}
		}
		mock.ExpectExec("^" + regexp.QuoteMeta(groupAppend+c.expected) + "$").WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := NewWithDB(db).AddErrorNotification(c.alert); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.args == 3 {
			if ls := lines.lines(t); len(ls) != 2 || string(ls[1].Args) != `{"name":"b.jpg"}` {
				t.Errorf("%s: msg_lines %+v", c.name, ls)
			}
		}
		db.Close()
	}
}

// A database a release behind (no msg columns): the English alone, as
// before - also when the column goes between the select and the insert.
func TestAddErrorNotificationOnAnOlderSchema(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	d := NewWithDB(db)
	legacySelect := "select `uuid`, coalesce\\(length\\(`details`\\), 0\\) from `notifications` where `type` = 'Error'"

	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnError(unknownColumn("msg_key"))
	mock.ExpectQuery(legacySelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n"}))
	mock.ExpectExec(regexp.QuoteMeta(plainErrorInsert)).WithArgs(sqlmock.AnyArg(), "", "x.jpg could not be read", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := d.AddErrorNotification(fileAlert("x.jpg", "EOF")); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnError(unknownColumn("msg_key"))
	mock.ExpectQuery(legacySelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n"}).AddRow("g1", 10))
	mock.ExpectExec("^"+regexp.QuoteMeta(groupAppend+" where `uuid` = ?")+"$").WithArgs(sqlmock.AnyArg(), "g1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := d.AddErrorNotification(fileAlert("y.jpg", "EOF")); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n", "msg_key", "msg_lines"}))
	mock.ExpectExec(regexp.QuoteMeta(keyedErrorInsert)).WillReturnError(unknownColumn("msg_key"))
	mock.ExpectExec(regexp.QuoteMeta(plainErrorInsert)).WithArgs(sqlmock.AnyArg(), "", "z.jpg could not be read", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := d.AddErrorNotification(fileAlert("z.jpg", "EOF")); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A standalone story: started with its keys, then each occurrence takes
// the new title's keys and adds its line; a row with only its English
// keeps none; an older schema gets the update it always got.
func TestUpsertErrorNotificationKeys(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	d := NewWithDB(db)
	first := alerttext.New(i18n.DevAlertMemoryRestarted(), i18n.DevAlertMemoryHintLowSmall(4))
	lines := &captured{}
	mock.ExpectExec(regexp.QuoteMeta(keyedErrorInsert)).
		WithArgs(sqlmock.AnyArg(), cStandaloneError, "Your device ran out of memory and restarted", sqlmock.AnyArg(), "dev.alert.memory.restarted", "{}", lines).
		WillReturnResult(sqlmock.NewResult(1, 1))
	id, err := d.UpsertErrorNotification("", first)
	if err != nil {
		t.Fatal(err)
	}
	ls := lines.lines(t)
	if len(ls) != 1 || ls[0].DetailKey != "dev.alert.memory.hint_low_small" || string(ls[0].DetailArgs) != `{"gb":4}` {
		t.Fatalf("first line %+v", ls)
	}

	again := alerttext.New(i18n.DevAlertMemoryRestartedTimes(2, "19:35"), i18n.Msg{})
	appended := &captured{}
	mock.ExpectQuery(standaloneSelect).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"n", "msg_key", "msg_lines"}).AddRow(200, "dev.alert.memory.restarted", lines.str()))
	mock.ExpectExec(regexp.QuoteMeta("update `notifications` set `title` = ?, `details` = concat(coalesce(`details`, ''), '\n', ?), `occurrences` = `occurrences` + 1, `acknowledged` = 0, `msg_key` = ?, `msg_args` = ?, `msg_lines` = ? where `uuid` = ? and `type` = 'Error'")).
		WithArgs("Your device ran out of memory and restarted 2 times since 19:35", sqlmock.AnyArg(), "dev.alert.memory.restarted_times", `{"clock":"19:35","count":2}`, appended, id).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if got, err := d.UpsertErrorNotification(id, again); err != nil || got != id {
		t.Fatalf("again: %q, %v", got, err)
	}
	if ls := appended.lines(t); len(ls) != 2 || ls[1].Key != "dev.alert.memory.restarted_times" {
		t.Errorf("lines after the second %+v", ls)
	}

	// Details full: the title and its keys change, nothing is appended.
	mock.ExpectQuery(standaloneSelect).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"n", "msg_key", "msg_lines"}).AddRow(errorNotificationDetailsCap, "dev.alert.memory.restarted_times", appended.str()))
	mock.ExpectExec(regexp.QuoteMeta("update `notifications` set `title` = ?, `details` = `details`, ")).
		WithArgs(sqlmock.AnyArg(), "dev.alert.memory.restarted_times", sqlmock.AnyArg(), appended.str(), id).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if _, err := d.UpsertErrorNotification(id, alerttext.New(i18n.DevAlertMemoryRestartedTimes(3, "19:35"), i18n.Msg{})); err != nil {
		t.Fatal(err)
	}

	// A row with only its English.
	mock.ExpectQuery(standaloneSelect).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"n", "msg_key", "msg_lines"}).AddRow(200, nil, nil))
	mock.ExpectExec(regexp.QuoteMeta("update `notifications` set `title` = ?")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), nil, nil, nil, id).WillReturnResult(sqlmock.NewResult(0, 1))
	if _, err := d.UpsertErrorNotification(id, again); err != nil {
		t.Fatal(err)
	}

	// An older schema.
	mock.ExpectQuery(standaloneSelect).WithArgs(id).WillReturnError(unknownColumn("msg_key"))
	mock.ExpectExec(regexp.QuoteMeta("update `notifications` set `title` = ?, `details` = if(")).
		WithArgs("Your device ran out of memory and restarted 2 times since 19:35", sqlmock.AnyArg(), errorNotificationDetailsCap, sqlmock.AnyArg(), id).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if got, err := d.UpsertErrorNotification(id, again); err != nil || got != id {
		t.Fatalf("older schema: %q, %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A storage alert's details are its detail alone, in msg_lines too.
func TestAddStorageNotificationKeys(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	d := NewWithDB(db)
	a := alerttext.New(i18n.DevAlertRaidStoppedTop(), i18n.DevAlertRaidStoppedDetailDisk("sdb"))
	lines := &captured{}
	mock.ExpectExec(regexp.QuoteMeta("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`, `msg_key`, `msg_args`, `msg_lines`) values (?, now(), 'Error', 'This device', '', ?, ?, 1, ?, ?, ?)")).
		WithArgs(sqlmock.AnyArg(), "The disk in the top blue USB port stopped working", a.Detail, "dev.alert.raid.stopped_top", "{}", lines).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.AddStorageNotification(a); err != nil {
		t.Fatal(err)
	}
	if ls := lines.lines(t); len(ls) != 1 || ls[0].At != "" || ls[0].Key != "" || ls[0].DetailKey != "dev.alert.raid.stopped_detail_disk" {
		t.Errorf("msg_lines %+v", ls)
	}

	// Older schema; and an alert with only its English.
	mock.ExpectExec("insert into `notifications`.*`msg_key`").WillReturnError(unknownColumn("msg_key"))
	mock.ExpectExec(regexp.QuoteMeta("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`) values")).
		WithArgs(sqlmock.AnyArg(), a.Title, a.Detail).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.AddStorageNotification(a); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(regexp.QuoteMeta("insert into `notifications` (`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`) values")).
		WithArgs(sqlmock.AnyArg(), "Two disks stopped", "d").WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.AddStorageNotification(alerttext.English("Two disks stopped", "d")); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// An update alert stores its keys with its version.
func TestAddUpdateNotificationKeys(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	a := alerttext.New(i18n.DevAlertUpdateCritical("3.0"), i18n.DevAlertUpdateDetail("A fix."))
	lines := &captured{}
	mock.ExpectQuery("select count").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `notifications`.*`update_version`, `msg_key`").
		WithArgs(sqlmock.AnyArg(), "Critical update 3.0 - please install it soon", "A fix. Install it from Settings.", "3.0", "dev.alert.update.critical", `{"version":"3.0"}`, lines).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if added, err := NewWithDB(db).AddUpdateNotification("3.0", a, nil); err != nil || !added {
		t.Fatalf("added %v, %v", added, err)
	}
	if ls := lines.lines(t); len(ls) != 1 || ls[0].DetailKey != "dev.alert.update.detail" || string(ls[0].DetailArgs) != `{"summary":"A fix."}` {
		t.Errorf("msg_lines %+v", ls)
	}
}

// A request without a language, or in English, is answered from
// ListNotifications' rows alone: not one more query.
func TestLocalizeNotificationsEnglishAsks(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	ns := []*pb.Notification{{Uuid: "n1", Type: pb.NotificationType_NotificationError, Title: "t", Details: "d"}}
	for _, lang := range []string{"", "en", "en-GB", "xx"} {
		if !englishOnly(lang) {
			continue
		}
		if err := NewWithDB(db).LocalizeNotifications(lang, ns); err != nil {
			t.Errorf("%q: %v", lang, err)
		}
	}
	if ns[0].Title != "t" || ns[0].Details != "d" {
		t.Errorf("changed: %+v", ns[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func englishOnly(lang string) bool {
	code := i18n.Normalize(lang)
	return code == "" || code == "en"
}

// otherLanguage is a language this build carries besides English (the
// pseudo-locale of a `make i18n DRAFT=1` build), or the test is skipped.
func otherLanguage(t *testing.T) string {
	t.Helper()
	code := ""
	for _, l := range i18n.Languages() {
		if l.Code == "qps" || (code == "" && l.Code != "en") {
			code = l.Code
		}
	}
	if code == "" {
		t.Skip("this build carries only English")
	}
	return code
}

// In another language: an alert as AddErrorNotification stored it is
// rendered from its keys; one whose key this build doesn't know, and
// friends' notifications, keep what they have.
func TestLocalizeNotificationsRendersStoredAlerts(t *testing.T) {
	lang := otherLanguage(t)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	d := NewWithDB(db)

	// Store one, keeping what was written.
	details, args, lines := &captured{}, &captured{}, &captured{}
	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n", "msg_key", "msg_lines"}))
	mock.ExpectExec(regexp.QuoteMeta(keyedErrorInsert)).WithArgs(sqlmock.AnyArg(), "", "IMG_1.jpg could not be read", details, "dev.alert.file.not_read", args, lines).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := d.AddErrorNotification(fileAlert("IMG_1.jpg", "EOF")); err != nil {
		t.Fatal(err)
	}

	ns := []*pb.Notification{
		{Uuid: "n1", Type: pb.NotificationType_NotificationError, Title: "IMG_1.jpg could not be read", Details: details.str()},
		{Uuid: "n2", Type: pb.NotificationType_NotificationError, Title: "from a newer release", Details: "its English"},
		{Uuid: "n3", Type: pb.NotificationType_NotificationLikePublication, ActorName: "Kim"},
		{Uuid: "n4", Type: pb.NotificationType_NotificationUpdate, Title: "old update", Details: "pre-118"},
	}
	mock.ExpectQuery(regexp.QuoteMeta("select `uuid`, `msg_key`, `msg_args`, `msg_lines` from `notifications` where `msg_key` is not null and `uuid` in (?, ?, ?)")).
		WithArgs("n1", "n2", "n4").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "msg_key", "msg_args", "msg_lines"}).
			AddRow("n1", "dev.alert.file.not_read", args.str(), lines.str()).
			AddRow("n2", "dev.alert.from_a_newer_release", "{}", "[]"))
	if err := d.LocalizeNotifications(lang, ns); err != nil {
		t.Fatal(err)
	}
	if want := i18n.DevAlertFileNotRead("IMG_1.jpg").Render(lang); ns[0].Title != want {
		t.Errorf("title %q, want %q", ns[0].Title, want)
	}
	if strings.Contains(ns[0].Details, "could not be read") || !strings.Contains(ns[0].Details, ": EOF") || !strings.Contains(ns[0].Details, "IMG_1.jpg") {
		t.Errorf("details %q", ns[0].Details)
	}
	if ns[1].Title != "from a newer release" || ns[1].Details != "its English" || ns[3].Title != "old update" || ns[3].Details != "pre-118" {
		t.Errorf("English rows changed: %+v %+v", ns[1], ns[3])
	}

	// A database without the columns: everything as it was.
	mock.ExpectQuery("select `uuid`, `msg_key`").WillReturnError(unknownColumn("msg_key"))
	ns2 := []*pb.Notification{{Uuid: "n1", Type: pb.NotificationType_NotificationError, Title: "t", Details: "d"}}
	if err := d.LocalizeNotifications(lang, ns2); err == nil || ns2[0].Title != "t" {
		t.Errorf("older schema: %v, %+v", err, ns2[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
