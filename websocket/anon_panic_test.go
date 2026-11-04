// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"
	"time"

	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A friend request with no profile - a few bytes anyone can send before
// signing in - is refused as any request no device would send, before
// anything is dialled or looked up. It used to be read unchecked: a panic
// and a stack trace in the log each time.
func TestFriendRequestWithoutAProfileIsRefused(t *testing.T) {
	ch := &connHandler{mg: &Manager{}}
	resp, closeConn := ch.processMessage(&pb.ReqEnvelope{Id: 7, Payload: &pb.ReqEnvelope_ReqFriendshipInterRequest{
		ReqFriendshipInterRequest: &pb.FriendshipInterRequest{Domain: "a.off-the.cloud", Secret: "x"}}})
	if resp.GetError() {
		t.Fatalf("answered as a failure (a panic?): %q", resp.GetErrorMessage())
	}
	ack := resp.GetRespAck()
	if ack == nil || ack.Ok || ack.ErrorMsg != i18n.DevSocialRequestRefusedError().Render("") || !closeConn {
		t.Fatalf("ack %+v, close %v", ack, closeConn)
	}
}

// A stranger's request that panics is logged with its stack once a minute;
// the rest are counted (anonLog). A signed-in owner's always is.
func TestPanicsBeforeSignInLoggedOnceAMinute(t *testing.T) {
	saved := anonPanics
	anonPanics = &everyGate{every: time.Minute}
	t.Cleanup(func() { anonPanics = saved })

	// mg.profile is nil: GetProfile panics.
	ch := &connHandler{mg: &Manager{}}
	before := log.ErrorCount()
	for range 5 {
		resp, closeConn := ch.processMessage(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqGetProfile{ReqGetProfile: &pb.GetProfile{}}})
		if !resp.GetError() || !closeConn {
			t.Fatalf("a panic answered %+v, close %v", resp, closeConn)
		}
	}
	if n := log.ErrorCount() - before; n != 1 {
		t.Errorf("%d error lines for five panics before sign-in, want 1", n)
	}

	before = log.ErrorCount()
	recoveredPanic(false, "boom", nil)
	recoveredPanic(false, "boom", nil)
	if n := log.ErrorCount() - before; n != 2 {
		t.Errorf("%d error lines for two signed-in panics, want 2", n)
	}
}

func TestEveryGate(t *testing.T) {
	g := &everyGate{every: time.Minute}
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	if !g.allow(at) || g.allow(at.Add(59*time.Second)) || !g.allow(at.Add(time.Minute)) || g.allow(at.Add(time.Minute+time.Second)) {
		t.Error("not once a minute")
	}
}
