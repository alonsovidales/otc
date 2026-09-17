// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
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

// GetPubKey must be handled pre-auth (issue #2): a client fetches the
// per-connection RSA public key and uses it to encrypt the password before
// it ever leaves the client, so the bridge only ever relays ciphertext.
func TestGetPubKeyGeneratesUsableKeypair(t *testing.T) {
	// GetPubKey's handler also checks whether the vault's secret is
	// defined yet (issue #39's IsNewDevice flag), so it needs a Dao behind
	// it — a mock connection stands in for a live DB. Two ExpectQuery
	// calls: the handler is exercised twice below (verifying keypair
	// reuse), and each hit re-runs the same query.
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	countRow := sqlmock.NewRows([]string{"count(*)"}).AddRow(0)
	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(countRow)
	countRow2 := sqlmock.NewRows([]string{"count(*)"}).AddRow(0)
	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(countRow2)

	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db)}}
	env := &pb.ReqEnvelope{
		Id:      1,
		Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}},
	}

	resp, closeConn := ch.processNonAuthRequest(env)

	if resp == nil {
		t.Fatal("expected a non-nil response")
	}
	if closeConn {
		t.Error("expected closeConn to be false")
	}
	if resp.Error {
		t.Fatalf("unexpected error response: %s", resp.ErrorMessage)
	}
	pubKeyResp, ok := resp.Payload.(*pb.RespEnvelope_RespPubKey)
	if !ok {
		t.Fatalf("expected a RespPubKey payload, got %T", resp.Payload)
	}
	if ch.privKey == nil {
		t.Fatal("expected the connection to keep the matching private key")
	}

	pubAny, err := x509.ParsePKIXPublicKey(pubKeyResp.RespPubKey.PublicKey)
	if err != nil {
		t.Fatalf("returned public key is not valid PKIX DER: %s", err)
	}
	pubKey, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("expected an RSA public key, got %T", pubAny)
	}
	if pubKey.N.Cmp(ch.privKey.PublicKey.N) != 0 {
		t.Error("returned public key does not match the connection's private key")
	}

	// A second call must reuse the same keypair rather than rotating it
	// mid-connection.
	resp2, _ := ch.processNonAuthRequest(env)
	pubKeyResp2 := resp2.Payload.(*pb.RespEnvelope_RespPubKey)
	if string(pubKeyResp2.RespPubKey.PublicKey) != string(pubKeyResp.RespPubKey.PublicKey) {
		t.Error("expected repeated GetPubKey calls on the same connection to return the same key")
	}
}

func TestDecryptSecretRoundTrip(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))

	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db)}}
	env := &pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}}
	resp, _ := ch.processNonAuthRequest(env)
	pubDER := resp.Payload.(*pb.RespEnvelope_RespPubKey).RespPubKey.PublicKey
	pubAny, _ := x509.ParsePKIXPublicKey(pubDER)
	pubKey := pubAny.(*rsa.PublicKey)

	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pubKey, []byte("s3cr3t"), nil)
	if err != nil {
		t.Fatalf("encrypting test ciphertext: %s", err)
	}

	plain, err := ch.decryptSecret(ciphertext)
	if err != nil {
		t.Fatalf("decryptSecret returned an error: %s", err)
	}
	if plain != "s3cr3t" {
		t.Errorf("expected decrypted secret %q, got %q", "s3cr3t", plain)
	}
}

func TestDecryptSecretWithoutPubKeyFails(t *testing.T) {
	ch := &connHandler{mg: &Manager{}}
	if _, err := ch.decryptSecret([]byte("not a valid ciphertext")); err == nil {
		t.Error("expected an error when no GetPubKey request preceded Auth")
	}
}

// Incident: the bridge pins one pool connection to a client for that
// client's whole session (see bridge/websocket/websocket.go), not just one
// request — a pool sized for quick per-request turnover emptied out under
// completely ordinary concurrent use (a phone app, a Mac app, a browser tab
// each holding one open at once) and surfaced as "No available connections
// in the pool" for every new session once it did. These lock in the
// fallback default and the low-water calculation without needing a real
// [otc] config section (cfg.HasSection safely reports false when cfg was
// never initialized, as in this test binary).
func TestBridgePoolTargetDefaultsWhenUnconfigured(t *testing.T) {
	if got := bridgePoolTarget(); got != cDefaultBridgePoolTarget {
		t.Errorf("expected default bridge pool target %d, got %d", cDefaultBridgePoolTarget, got)
	}
}

func TestBridgePoolLowWaterIsRefillBatchBelowTarget(t *testing.T) {
	want := cDefaultBridgePoolTarget - cBridgePoolRefillBatch
	if got := bridgePoolLowWater(); got != want {
		t.Errorf("expected low water %d, got %d", want, got)
	}
}

// A connection's requests are now processed concurrently (see
// handleConnection's doc comment) instead of one at a time, so
// connHandler's session/friendProfile/privKey - each set once by one
// request and read by many others - need a real, run-with-race-detector
// guarantee, not just "it happens to work because setting one is fast".
// getOrCreatePrivKey is the one with an actual check-then-act step
// (generate only if nil), which is exactly the shape of bug an unguarded
// version would have: two GetPubKey requests racing each other could each
// see nil and generate their own keypair, with one silently overwriting
// the other - a client that got the discarded one back would fail every
// decrypt for the rest of the connection.
func TestGetOrCreatePrivKeyIsSingletonUnderConcurrentCallers(t *testing.T) {
	ch := &connHandler{}
	const n = 30
	keys := make([]*rsa.PrivateKey, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			keys[i], errs[i] = ch.getOrCreatePrivKey()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: unexpected error: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if keys[i] != keys[0] {
			t.Errorf("expected every concurrent caller to get back the same keypair, caller %d got a different one", i)
		}
	}
}

func TestSessionAndFriendProfileAccessorsAreRaceSafe(t *testing.T) {
	ch := &connHandler{}
	ses := &session.Session{}
	prof := &profile.Profile{}

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(4)
		go func() { defer wg.Done(); ch.setSession(ses) }()
		go func() { defer wg.Done(); ch.getSession() }()
		go func() { defer wg.Done(); ch.setFriendProfile(prof) }()
		go func() { defer wg.Done(); ch.getFriendProfile() }()
	}
	wg.Wait()

	if ch.getSession() != ses {
		t.Error("expected getSession to return the session that was set")
	}
	if ch.getFriendProfile() != prof {
		t.Error("expected getFriendProfile to return the profile that was set")
	}
}
