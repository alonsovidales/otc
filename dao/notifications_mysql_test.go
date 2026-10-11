// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// notificationsTestDB is searchTestDB with a temporary `notifications`
// table shaped as db.sql's - with release 118's columns, or without them
// (a database its release script hasn't reached).
func notificationsTestDB(tb testing.TB, withMsg bool) *Dao {
	tb.Helper()
	db := searchTestDB(tb)
	cols := "`uuid` varchar(64) not null, `dt` datetime not null, `type` varchar(32) not null, `actor_name` varchar(255) not null, " +
		"`actor_domain` varchar(128) not null, `pub_uuid` varchar(64) default null, `comment_uuid` varchar(64) default null, " +
		"`acknowledged` tinyint(1) not null default 0, `title` varchar(255) default null, `details` text default null, " +
		"`occurrences` int not null default 1, "
	if withMsg {
		cols += "`msg_key` varchar(96) null, `msg_args` mediumtext null, `msg_lines` mediumtext null, `update_version` varchar(16) null, "
	}
	if _, err := db.Exec("create temporary table `notifications` (" + cols + "unique(`uuid`), index using btree (`acknowledged`), index using btree (`dt`)) engine=InnoDB"); err != nil {
		tb.Fatal(err)
	}
	return NewWithDB(db)
}

type storedRow struct {
	title, details      string
	occurrences         int
	key, args, lines    sql.NullString
	domain, typ, update sql.NullString
}

func readRows(t *testing.T, d *Dao) []storedRow {
	t.Helper()
	rows, err := d.db.Query("select `title`, `details`, `occurrences`, `msg_key`, `msg_args`, `msg_lines`, `actor_domain`, `type`, `update_version` from `notifications` order by `dt`, `title`")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedRow
	for rows.Next() {
		var r storedRow
		if err := rows.Scan(&r.title, &r.details, &r.occurrences, &r.key, &r.args, &r.lines, &r.domain, &r.typ, &r.update); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func lineCount(t *testing.T, s sql.NullString) int {
	t.Helper()
	var ls []alerttext.Line
	if err := json.Unmarshal([]byte(s.String), &ls); err != nil {
		t.Fatalf("msg_lines %q: %v", s.String, err)
	}
	return len(ls)
}

// Every write, as MariaDB takes it: keys and lines next to the English,
// grouped and standalone, a group made English whole, storage and update
// alerts; then rendered back.
func TestAlertKeysMySQL(t *testing.T) {
	d := notificationsTestDB(t, true)
	if err := d.AddErrorNotification(alerttext.WithRaw(i18n.DevAlertFileNotRead("a.jpg"), "EOF")); err != nil {
		t.Fatal(err)
	}
	if err := d.AddErrorNotification(alerttext.WithRaw(i18n.DevAlertFileNotSaved("b.jpg"), "disk full")); err != nil {
		t.Fatal(err)
	}
	id, err := d.UpsertErrorNotification("", alerttext.New(i18n.DevAlertMemoryRestarted(), i18n.DevAlertMemoryHint(8)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpsertErrorNotification(id, alerttext.New(i18n.DevAlertMemoryRestartedTimes(2, "19:35"), i18n.Msg{})); err != nil {
		t.Fatal(err)
	}
	if g := readRowsByTitle(t, d)["a.jpg could not be read"]; g.key.String != "dev.alert.file.not_read" || lineCount(t, g.lines) != 2 {
		t.Fatalf("group %+v", g)
	}
	// An error with only its English joins: the group is English whole.
	if err := d.AddErrorNotification(alerttext.English("c.jpg broke", "x")); err != nil {
		t.Fatal(err)
	}
	if g := readRowsByTitle(t, d)["a.jpg could not be read"]; g.key.Valid || g.args.Valid || g.lines.Valid || g.occurrences != 3 || !strings.HasSuffix(g.details, " c.jpg broke: x") {
		t.Errorf("after an English-only error: %+v", g)
	}
	// (A grouped error would join a storage row too: it is an Error row
	// with an empty actor_domain - as before keys. Not exercised here.)
	if err := d.AddStorageNotification(alerttext.New(i18n.DevAlertRaidStoppedTop(), i18n.DevAlertRaidStoppedDetail())); err != nil {
		t.Fatal(err)
	}
	if added, err := d.AddUpdateNotification("3.0", alerttext.New(i18n.DevAlertUpdateMajor("3.0"), i18n.DevAlertUpdateDetail("More.")), []string{"Update 3.0 is available"}); err != nil || !added {
		t.Fatalf("update: %v, %v", added, err)
	}
	rows := readRows(t, d)
	if len(rows) != 4 {
		t.Fatalf("%d rows: %+v", len(rows), rows)
	}
	byTitle := map[string]storedRow{}
	for _, r := range rows {
		byTitle[r.title] = r
	}
	if group := byTitle["a.jpg could not be read"]; group.occurrences != 3 || !strings.Contains(group.details, " b.jpg could not be saved to disk: disk full\n") {
		t.Errorf("group %+v", group)
	}
	memory := byTitle["Your device ran out of memory and restarted 2 times since 19:35"]
	if memory.domain.String != cStandaloneError || memory.key.String != "dev.alert.memory.restarted_times" || memory.args.String != `{"clock": "19:35", "count": 2}` && memory.args.String != `{"clock":"19:35","count":2}` || lineCount(t, memory.lines) != 2 {
		t.Errorf("memory %+v", memory)
	}
	storage := byTitle["The disk in the top blue USB port stopped working"]
	if storage.key.String != "dev.alert.raid.stopped_top" || lineCount(t, storage.lines) != 1 || storage.details != alerttext.EnglishOf(i18n.DevAlertRaidStoppedDetail()) {
		t.Errorf("storage %+v", storage)
	}
	update := byTitle["Update 3.0 is available"]
	if update.typ.String != "Update" || update.update.String != "3.0" || update.key.String != "dev.alert.update.major" || update.details != "More. Install it from Settings." {
		t.Errorf("update %+v", update)
	}

	// Rendered back: English as stored; another language when this build
	// carries one (DRAFT).
	ns := []*pb.Notification{{Uuid: "", Type: pb.NotificationType_NotificationError}}
	if err := d.db.QueryRow("select `uuid`, `title`, `details` from `notifications` where `msg_key` = 'dev.alert.raid.stopped_top'").Scan(&ns[0].Uuid, &ns[0].Title, &ns[0].Details); err != nil {
		t.Fatal(err)
	}
	if err := d.LocalizeNotifications("", ns); err != nil || ns[0].Title != "The disk in the top blue USB port stopped working" {
		t.Errorf("English: %v %+v", err, ns[0])
	}
	for _, l := range i18n.Languages() {
		if l.Code == "en" {
			continue
		}
		n := &pb.Notification{Uuid: ns[0].Uuid, Type: pb.NotificationType_NotificationError, Title: ns[0].Title, Details: ns[0].Details}
		if err := d.LocalizeNotifications(l.Code, []*pb.Notification{n}); err != nil || n.Title != i18n.DevAlertRaidStoppedTop().Render(l.Code) || n.Details != i18n.DevAlertRaidStoppedDetail().Render(l.Code) {
			t.Errorf("%s: %v %+v", l.Code, err, n)
		}
	}
}

func readRowsByTitle(t *testing.T, d *Dao) map[string]storedRow {
	t.Helper()
	m := map[string]storedRow{}
	for _, r := range readRows(t, d) {
		m[r.title] = r
	}
	return m
}

// A database without release 118's columns takes every write, in English,
// and renders it as stored.
func TestAlertKeysOnAnOlderSchemaMySQL(t *testing.T) {
	d := notificationsTestDB(t, false)
	if err := d.AddErrorNotification(alerttext.WithRaw(i18n.DevAlertFileNotRead("a.jpg"), "EOF")); err != nil {
		t.Fatal(err)
	}
	if err := d.AddErrorNotification(alerttext.WithRaw(i18n.DevAlertFileNotSaved("b.jpg"), "disk full")); err != nil {
		t.Fatal(err)
	}
	id, err := d.UpsertErrorNotification("", alerttext.New(i18n.DevAlertMemoryRestarted(), i18n.DevAlertMemoryHint(8)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := d.UpsertErrorNotification(id, alerttext.New(i18n.DevAlertMemoryRestartedTimes(2, "19:35"), i18n.Msg{})); err != nil || got != id {
		t.Fatalf("upsert: %q, %v", got, err)
	}
	if err := d.AddStorageNotification(alerttext.New(i18n.DevAlertRaidStoppedTop(), i18n.DevAlertRaidStoppedDetail())); err != nil {
		t.Fatal(err)
	}
	if added, err := d.AddUpdateNotification("3.0", alerttext.New(i18n.DevAlertUpdateMajor("3.0"), i18n.DevAlertUpdateDetail("More.")), []string{"Update 3.0 is available"}); err != nil || !added {
		t.Fatalf("update: %v, %v", added, err)
	}
	var n int
	var occ int
	if err := d.db.QueryRow("select count(*), sum(`occurrences`) from `notifications`").Scan(&n, &occ); err != nil || n != 4 || occ != 6 {
		t.Errorf("rows %d, occurrences %d, %v", n, occ, err)
	}
	for _, l := range i18n.Languages() {
		ns := []*pb.Notification{{Uuid: id, Type: pb.NotificationType_NotificationError, Title: "t", Details: "d"}}
		err := d.LocalizeNotifications(l.Code, ns)
		if ns[0].Title != "t" || (l.Code != "en" && err == nil) {
			t.Errorf("%s: %v %+v", l.Code, err, ns[0])
		}
	}
}
