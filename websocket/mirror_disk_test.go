// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/raidwatch"
	"github.com/alonsovidales/otc/supervisor"
)

// mirrorSeams stands raidwatch in: the disks offered, and the disks asked
// for (refused with refuse when set).
type mirrorSeams struct {
	offered []raidwatch.Candidate
	asked   []string
	refuse  error
}

func withMirror(t *testing.T) *mirrorSeams {
	t.Helper()
	s := &mirrorSeams{offered: []raidwatch.Candidate{{ID: "0123456789abcdef0123", Model: "Generic Card Reader", Size: 512_110_190_592}}}
	savedCandidates, savedUse := mirrorCandidates, mirrorUseDisk
	t.Cleanup(func() { mirrorCandidates, mirrorUseDisk = savedCandidates, savedUse })
	mirrorCandidates = func() []raidwatch.Candidate { return s.offered }
	mirrorUseDisk = func(id string) error {
		if s.refuse != nil {
			return s.refuse
		}
		s.asked = append(s.asked, id)
		return nil
	}
	return s
}

func useDiskReq(id int32, disk string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: id, Payload: &pb.ReqEnvelope_ReqUseMirrorDisk{ReqUseMirrorDisk: &pb.UseMirrorDisk{Disk: disk}}}
}

func signedInOwner(t *testing.T, mg *Manager) *connHandler {
	t.Helper()
	ch := &connHandler{mg: mg}
	ch.setSession(newTestAuthenticatedSession(t))
	return ch
}

// The owner of the primary instance asks; the answer is the generic Ack,
// at once.
func TestUseMirrorDiskOnThePrimary(t *testing.T) {
	s := withMirror(t)
	ch := signedInOwner(t, &Manager{sup: &supervisor.Supervisor{}})
	resp := mustProcess(t, ch, useDiskReq(2, "0123456789abcdef0123"))
	if ack := resp.GetRespAck(); resp.Error || ack == nil || !ack.Ok || ack.Code != "" {
		t.Fatalf("%+v", resp)
	}
	if len(s.asked) != 1 || s.asked[0] != "0123456789abcdef0123" {
		t.Errorf("asked %q", s.asked)
	}
}

// What raidwatch refuses - not a disk offered now, a mirror that doesn't
// lack one, one asked a moment ago - comes back with its code, in the Ack
// and on the envelope, in the request's language; any other failure
// without a code.
func TestUseMirrorDiskRefusals(t *testing.T) {
	s := withMirror(t)
	ch := signedInOwner(t, &Manager{sup: &supervisor.Supervisor{}})
	for i, c := range []struct {
		code string
		msg  i18n.Msg
	}{
		{"not_candidate", i18n.DevDeviceMirrorNotCandidate()},
		{"not_degraded", i18n.DevDeviceMirrorNotDegraded()},
		{"busy", i18n.DevDeviceMirrorBusy()},
	} {
		for _, lang := range []string{"", "es"} {
			s.refuse = &raidwatch.Refusal{Code: c.code, Msg: c.msg}
			env := useDiskReq(int32(10+i), "fedcba9876543210fedc")
			env.Lang = lang
			resp := mustProcess(t, ch, env)
			ack := resp.GetRespAck()
			want := c.msg.Render(lang)
			if !resp.Error || resp.ErrorCode != c.code || resp.ErrorMessage != want || ack == nil || ack.Ok || ack.Code != c.code || ack.ErrorMsg != want {
				t.Errorf("%s (%q): %+v", c.code, lang, resp)
			}
		}
	}
	if want := "That disk is no longer offered for the mirror: it was unplugged, or it was added already."; i18n.DevDeviceMirrorNotCandidate().Render("") != want {
		t.Errorf("English: %q", i18n.DevDeviceMirrorNotCandidate().Render(""))
	}
	if es := i18n.DevDeviceMirrorNotDegraded().Render("es"); es == i18n.DevDeviceMirrorNotDegraded().Render("") {
		t.Errorf("no Spanish: %q", es)
	}
	s.refuse = errors.New("read-only file system")
	resp := mustProcess(t, ch, useDiskReq(20, "fedcba9876543210fedc"))
	if ack := resp.GetRespAck(); !resp.Error || resp.ErrorCode != "" || resp.ErrorMessage != "read-only file system" || ack == nil || ack.Ok || ack.Code != "" {
		t.Errorf("a failure: %+v", resp)
	}
	if len(s.asked) != 0 {
		t.Errorf("asked %q", s.asked)
	}
}

// Who may: never a user's instance (the machine's disks aren't theirs), a
// connection that hasn't signed in or a friend's device, a container (the
// server's disks) or a demo device - none asks anything.
func TestUseMirrorDiskGates(t *testing.T) {
	s := withMirror(t)

	child := signedInOwner(t, &Manager{})
	resp := mustProcess(t, child, useDiskReq(2, "0123456789abcdef0123"))
	if !resp.Error || resp.ErrorCode != "" || resp.ErrorMessage != "not available on this instance" {
		t.Errorf("a user's instance: %+v", resp)
	}

	if resp, _ := (&connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}).processMessage(useDiskReq(3, "0123456789abcdef0123")); resp != nil {
		t.Errorf("not signed in: %+v", resp)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `status`, `forget_requested` is not null from `social_friendship`").
		WillReturnRows(sqlmock.NewRows([]string{"status", "leaving"}).AddRow("accepted", false))
	friend := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}, dao: dao.NewWithDB(db)}}
	friend.setFriendProfile(&profile.Profile{})
	if resp, _ := friend.processMessage(useDiskReq(4, "0123456789abcdef0123")); resp != nil {
		t.Errorf("a friend: %+v", resp)
	}

	t.Run("container", func(t *testing.T) {
		inContainer(t)
		ch := signedInOwner(t, &Manager{sup: &supervisor.Supervisor{}})
		resp := mustProcess(t, ch, useDiskReq(5, "0123456789abcdef0123"))
		if !resp.Error || !strings.HasPrefix(resp.ErrorMessage, "Not available in Docker") {
			t.Errorf("container: %+v", resp)
		}
	})
	t.Run("demo", func(t *testing.T) {
		asDemo(t)
		ch := signedInOwner(t, &Manager{sup: &supervisor.Supervisor{}})
		resp := mustProcess(t, ch, useDiskReq(6, "0123456789abcdef0123"))
		if !resp.Error || resp.ErrorCode != cCodeDemo || resp.GetRespAck().GetCode() != cCodeDemo {
			t.Errorf("demo: %+v", resp)
		}
	})
	if len(s.asked) != 0 {
		t.Errorf("asked %q", s.asked)
	}
}

const cStatusMirrorChildEnv = "OTC_WS_STATUS_MIRROR_CHILD"

// Status lists the disks offered to the owner of the primary instance,
// with what names them and nothing more; a user's instance and a demo
// device list none.
func TestStatusOffersMirrorDisks(t *testing.T) {
	if os.Getenv(cStatusMirrorChildEnv) != "1" {
		// In a child process: GetStatus reads [otc] storage-path, and a
		// loaded config can't be unloaded (searchPhotosStorage).
		name := "TestStatusOffersMirrorDisks"
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), cStatusMirrorChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("in its own process: %v\n%s", err, out)
		}
		return
	}
	searchPhotosStorage(t)
	withMirror(t)
	status := func(ch *connHandler) *pb.Status {
		t.Helper()
		st := mustProcess(t, ch, &pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}}}).GetRespStatus()
		if st == nil {
			t.Fatal("no Status")
		}
		return st
	}
	mg, _ := languageManager(t, "", "")
	mg.sup = &supervisor.Supervisor{}
	st := status(signedInOwner(t, mg))
	if len(st.MirrorCandidates) != 1 {
		t.Fatalf("primary: %+v", st.MirrorCandidates)
	}
	if c := st.MirrorCandidates[0]; c.Id != "0123456789abcdef0123" || c.Model != "Generic Card Reader" || c.Size != 512_110_190_592 {
		t.Errorf("the disk: %+v", c)
	}

	child, _ := languageManager(t, "", "")
	if st := status(signedInOwner(t, child)); len(st.MirrorCandidates) != 0 {
		t.Errorf("a user's instance: %+v", st.MirrorCandidates)
	}

	asDemo(t)
	demo, _ := languageManager(t, "", "")
	demo.sup = &supervisor.Supervisor{}
	if st := status(signedInOwner(t, demo)); len(st.MirrorCandidates) != 0 || !st.Demo {
		t.Errorf("a demo device: %+v", st.MirrorCandidates)
	}
}
