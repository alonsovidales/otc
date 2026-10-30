// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

const cStatusBackgroundChildEnv = "OTC_WS_STATUS_BACKGROUND_CHILD"

// Status lists what the instance is working through on its library
// (release 152), in the request's language, and nothing once it is done.
func TestStatusListsBackgroundWork(t *testing.T) {
	if os.Getenv(cStatusBackgroundChildEnv) != "1" {
		// In a child process: GetStatus reads [otc] storage-path, and a
		// loaded config can't be unloaded (searchPhotosStorage).
		name := "TestStatusListsBackgroundWork"
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), cStatusBackgroundChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("in its own process: %v\n%s", err, out)
		}
		return
	}
	searchPhotosStorage(t)
	mg, _ := languageManager(t, "", "")
	ch := signedInOwner(t, mg)
	status := func(lang string) []*pb.BackgroundTask {
		t.Helper()
		st := mustProcess(t, ch, &pb.ReqEnvelope{Id: 2, Lang: lang, Payload: &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}}}).GetRespStatus()
		if st == nil {
			t.Fatal("no Status")
		}
		return st.BackgroundTasks
	}
	// No files manager (a test's Manager): nothing, and no panic.
	if got := status("en"); len(got) != 0 {
		t.Errorf("without a files manager: %v", got)
	}
	mg.filesManager = filesmanager.NewWithDAO(mg.dao)
	smart := mg.filesManager.BeginBackground(filesmanager.BackgroundSmartSearch, 120)
	smart.Step(30)
	uploads := mg.filesManager.BeginBackground(filesmanager.BackgroundAnalysis, 0)
	got := status("es")
	if len(got) != 2 ||
		got[0].Kind != "analysis" || got[0].Text != i18n.DevStatusBackgroundAnalysis().Render("es") || got[0].Total != 0 ||
		got[1].Kind != "smart_search" || got[1].Text != "Conociendo tus fotos" || got[1].Done != 30 || got[1].Total != 120 {
		t.Errorf("in Spanish: %v", got)
	}
	smart.End()
	uploads.End()
	if got := status("en"); len(got) != 0 {
		t.Errorf("once done: %v", got)
	}
}
