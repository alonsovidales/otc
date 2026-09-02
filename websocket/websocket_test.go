package websocket

import (
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// handleConnection dispatches every incoming envelope through up to three
// handlers in order: processNonAuthRequest, then (if a session or friend
// profile exists) processAuthAsFriendRequest, then (if a session exists)
// processAuthRequest. Each handler only recognizes a specific subset of
// request payload types; for the first two, returning (nil, false) on an
// unrecognized type is the deliberate "try the next handler" signal, not a
// failure. Only the last handler in the chain has nowhere further to fall
// through to, so it must always return a non-nil response.
//
// These tests pick, for each handler, a payload type that handler does not
// recognize (but that a sibling handler does) to exercise that dispatch
// contract without needing a live DB or social/files manager.

func TestProcessNonAuthRequestDefersUnhandledPayload(t *testing.T) {
	ch := &connHandler{mg: &Manager{}}
	// ReqGetStatus is only handled by processAuthRequest.
	env := &pb.ReqEnvelope{
		Id:      42,
		Payload: &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}},
	}

	resp, closeConn := ch.processNonAuthRequest(env)

	if resp != nil {
		t.Errorf("expected a nil response so the caller tries the next handler, got %+v", resp)
	}
	if closeConn {
		t.Error("expected closeConn to be false when deferring to the next handler")
	}
}

func TestProcessAuthAsFriendRequestDefersUnhandledPayload(t *testing.T) {
	ch := &connHandler{mg: &Manager{}}
	// ReqGetFriendshipStatus is only handled by processNonAuthRequest.
	env := &pb.ReqEnvelope{
		Id: 7,
		Payload: &pb.ReqEnvelope_ReqGetFriendshipStatus{
			ReqGetFriendshipStatus: &pb.GetFriendshipStatus{},
		},
	}

	resp, closeConn := ch.processAuthAsFriendRequest(env)

	if resp != nil {
		t.Errorf("expected a nil response so the caller tries the next handler, got %+v", resp)
	}
	if closeConn {
		t.Error("expected closeConn to be false when deferring to the next handler")
	}
}

func TestProcessAuthRequestAcksUnhandledPayload(t *testing.T) {
	// processAuthRequest is the last handler in the chain: it has nowhere
	// further to defer to, so it must always return a non-nil response
	// rather than silently dropping the request.
	ch := &connHandler{mg: &Manager{}}
	// ReqGetFriendshipStatus is only handled by processNonAuthRequest.
	env := &pb.ReqEnvelope{
		Id: 99,
		Payload: &pb.ReqEnvelope_ReqGetFriendshipStatus{
			ReqGetFriendshipStatus: &pb.GetFriendshipStatus{},
		},
	}

	resp, closeConn := ch.processAuthRequest(env)

	if resp == nil {
		t.Fatal("expected a non-nil response echoing the request id, since this is the last handler in the chain")
	}
	if resp.Id != env.Id {
		t.Errorf("expected response Id %d, got %d", env.Id, resp.Id)
	}
	if resp.Payload != nil {
		t.Errorf("expected no payload for an unrecognized request type, got %+v", resp.Payload)
	}
	if closeConn {
		t.Error("expected closeConn to be false for an unrecognized request type")
	}
}
