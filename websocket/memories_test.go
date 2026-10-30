// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// On this day: GetMemories answers the earlier years' memories (kept for
// the day: asked again, nothing is read), HideMemory hides and refuses
// what names nothing, and once SetMemoriesEnabled turns it off
// GetMemories answers memories_off.
func TestMemoriesRequests(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := func(i int) string { return fmt.Sprintf("%064x", 0x3e300+i) }
	at := func(hour, min int) time.Time { return time.Date(2023, 10, 30, hour, min, 0, 0, time.UTC) }
	quality := func(r *sqlmock.Rows, hash, path, mime string, t time.Time, camera int) *sqlmock.Rows {
		return r.AddRow(hash, path, mime, t, hash, 5.9, 220.0, 0.01, 0.02, 120.0, 1000, 750, camera)
	}
	cols := []string{"hash", "path", "mime", "created", "qh", "a", "s", "d", "b", "br", "w", "hh", "c"}
	rows := sqlmock.NewRows(cols)
	quality(rows, h(1), "/Phone/IMG_1.jpg", "image/jpeg", at(10, 0), 1)
	quality(rows, h(2), "/Phone/IMG_2.jpg", "image/jpeg", at(10, 30), 1)
	quality(rows, h(3), "/Phone/Screenshot_1.png", "image/png", at(11, 0), 0)

	mock.ExpectQuery("select `memories_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"m"}).AddRow(1))
	mock.ExpectQuery("select `face_recognition_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"f"}).AddRow(1))
	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}))
	mock.ExpectQuery("from `files` as `f` left join `photo_quality` as `q`").
		WithArgs("10-29", "10-30", "10-31").WillReturnRows(rows)
	mock.ExpectQuery("select `hash`, min\\(`created`\\) from `files` where `hash` in").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "c"}).AddRow(h(1), at(10, 0)).AddRow(h(2), at(10, 30)).AddRow(h(3), at(11, 0)))
	mock.ExpectQuery("select `kind`, `value` from `memories_hidden`").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "value"}).AddRow("person", "someone"))
	mock.ExpectQuery("from `file_tags` where `hash` in").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "tag", "score"}).AddRow(h(1), "beach", 0.9).AddRow(h(1), "Valencia", 1).AddRow(h(2), "Valencia", 1))
	mock.ExpectQuery("from `faces` as `f`").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "person", "name"}).AddRow(h(2), "p1", "Lucía"))
	mock.ExpectQuery("from `image_group_files`").WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	mock.ExpectQuery("from `social_publications_files`").WillReturnRows(sqlmock.NewRows([]string{"hash", "p", "l", "c"}))
	mock.ExpectQuery("from `file_tags` where `score` >= 1").
		WillReturnRows(sqlmock.NewRows([]string{"tag", "n"}).AddRow("Madrid", 40).AddRow("Spain", 50))

	ch := &connHandler{mg: &Manager{filesManager: filesmanager.NewWithDAO(dao.NewWithDB(db))}}
	ch.setSession(newTestAuthenticatedSession(t))
	get := &pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqGetMemories{ReqGetMemories: &pb.GetMemories{
		Date: "2026-10-30", Period: pb.MemoryPeriod_MemoryDay, TimeZone: "UTC",
	}}}
	resp, closeConn := ch.processMessage(get)
	if closeConn || resp.GetError() {
		t.Fatalf("GetMemories: %+v", resp)
	}
	ms := resp.GetRespMemories()
	if len(ms.GetMemories()) != 1 || ms.HiddenCount != 1 {
		t.Fatalf("answer %+v, want one memory and one thing hidden", ms)
	}
	m := ms.Memories[0]
	if m.YearsAgo != 3 || m.Key != "day:2023-10-30" || m.StartDate != "2023-10-30" || m.EndDate != "2023-10-30" ||
		m.Place != "Valencia" || len(m.People) != 1 || m.People[0] != "Lucía" || m.PhotoCount != 3 {
		t.Errorf("memory %+v", m)
	}
	if len(m.Files) != 2 || m.Files[0].Path != "/Phone/IMG_1.jpg" || m.Files[1].Path != "/Phone/IMG_2.jpg" || m.Cover.GetPath() != "/Phone/IMG_2.jpg" {
		t.Errorf("files %v cover %v, want both photos in order (no screenshot), Lucía's the cover", m.Files, m.Cover)
	}
	// Kept for the day: only whether face recognition is on is read again.
	mock.ExpectQuery("select `face_recognition_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"f"}).AddRow(1))
	if resp, _ := ch.processMessage(get); len(resp.GetRespMemories().GetMemories()) != 1 {
		t.Errorf("asked again: %+v", resp)
	}

	mock.ExpectExec("insert into `memories_hidden`").WithArgs("photo", h(1), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 4, Payload: &pb.ReqEnvelope_ReqHideMemory{ReqHideMemory: &pb.HideMemory{Kind: pb.MemoryHideKind_MemoryHidePhoto, Value: h(1)}}})
	if !resp.GetRespAck().GetOk() {
		t.Errorf("HideMemory: %+v", resp)
	}
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 5, Payload: &pb.ReqEnvelope_ReqHideMemory{ReqHideMemory: &pb.HideMemory{Kind: pb.MemoryHideKind_MemoryHideDay, Value: "yesterday"}}})
	if !resp.GetError() || resp.GetErrorMessage() == "" {
		t.Errorf("HideMemory of nothing: %+v", resp)
	}
	mock.ExpectExec("delete from `memories_hidden`$").WillReturnResult(sqlmock.NewResult(0, 2))
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 6, Payload: &pb.ReqEnvelope_ReqHideMemory{ReqHideMemory: &pb.HideMemory{ShowAll: true}}})
	if !resp.GetRespAck().GetOk() {
		t.Errorf("show all: %+v", resp)
	}

	mock.ExpectExec("update `settings` set `memories_enabled` = ?").WithArgs(false).WillReturnResult(sqlmock.NewResult(0, 1))
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 7, Payload: &pb.ReqEnvelope_ReqSetMemoriesEnabled{ReqSetMemoriesEnabled: &pb.SetMemoriesEnabled{Enabled: false}}})
	if !resp.GetRespAck().GetOk() {
		t.Errorf("SetMemoriesEnabled off: %+v", resp)
	}
	resp, _ = ch.processMessage(get)
	if !resp.GetError() || resp.GetErrorCode() != cCodeMemoriesOff || resp.GetErrorMessage() == "" {
		t.Errorf("GetMemories while off: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
