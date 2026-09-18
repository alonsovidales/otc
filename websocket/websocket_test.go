// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/social"
	"github.com/alonsovidales/otc/supervisor"
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

// Issue #102: ReqDidSendFriendshipReq is the anti-spoofing check behind
// ExternalFriendshipRequest - a friend's device calls this back to confirm
// we actually hold a friendship record for its domain+secret before it
// accepts a request claiming to come from us. GetFriendship only ever
// returns (nil, err) or (non-nil, nil), never any other combination, so the
// old `err != nil && friendship == nil` rejection happened to work in
// practice - but only by accident of that implementation detail, not
// because the check was correct on its own terms (see ReqAuthAsFriend's
// sibling check just above, which correctly uses `||`). These pin down the
// two cases directly.
func TestDidSendFriendshipReqRejectsWhenNoFriendshipRecordExists(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `status`, `name`, `image`, `text`, `sent` from `social_friendship`").
		WithArgs("stranger.otc", "bad-secret").
		WillReturnError(sql.ErrNoRows)

	ch := &connHandler{mg: &Manager{social: social.Init(dao.NewWithDB(db), nil, nil, nil, nil)}}
	env := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqDidSendFriendshipReq{
			ReqDidSendFriendshipReq: &pb.DidSendFriendshipReq{Domain: "stranger.otc", Secret: "bad-secret"},
		},
	}

	resp, closeConn := ch.processNonAuthRequest(env)

	if resp == nil {
		t.Fatal("expected a non-nil response")
	}
	if !closeConn {
		t.Error("expected the connection to be closed after rejecting")
	}
	ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck)
	if !ok {
		t.Fatalf("expected a RespAck payload, got %T", resp.Payload)
	}
	if ack.RespAck.Ok {
		t.Error("expected Ok=false when no friendship record exists for the domain+secret")
	}
}

func TestDidSendFriendshipReqAcceptsWhenFriendshipRecordExists(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	rows := sqlmock.NewRows([]string{"status", "name", "image", "text", "sent"}).
		AddRow("pending", "Friend Name", []byte(nil), "bio", true)
	mock.ExpectQuery("select `status`, `name`, `image`, `text`, `sent` from `social_friendship`").
		WithArgs("friend.otc", "real-secret").
		WillReturnRows(rows)

	ch := &connHandler{mg: &Manager{social: social.Init(dao.NewWithDB(db), nil, nil, nil, nil)}}
	env := &pb.ReqEnvelope{
		Id: 2,
		Payload: &pb.ReqEnvelope_ReqDidSendFriendshipReq{
			ReqDidSendFriendshipReq: &pb.DidSendFriendshipReq{Domain: "friend.otc", Secret: "real-secret"},
		},
	}

	resp, closeConn := ch.processNonAuthRequest(env)

	if resp == nil {
		t.Fatal("expected a non-nil response")
	}
	if closeConn {
		t.Error("expected the connection to stay open on success")
	}
	ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck)
	if !ok {
		t.Fatalf("expected a RespAck payload, got %T", resp.Payload)
	}
	if !ack.RespAck.Ok {
		t.Errorf("expected Ok=true when a friendship record exists, got error: %s", ack.RespAck.ErrorMsg)
	}
}

// newTestAuthenticatedSession builds a real *session.Session the same way
// a successful ReqAuth would (vault creation, Argon2id, the lot) via a
// mocked "brand new device" vault, so issue #101's token handlers below
// are exercised against the exact object type setSession actually stores,
// not a stand-in.
func newTestAuthenticatedSession(t *testing.T) *session.Session {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	mock.ExpectExec("insert into `vault`").WillReturnResult(sqlmock.NewResult(1, 1))

	ses, err := session.New("owner-uuid", "test-password", true, dao.NewWithDB(db))
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	return ses
}

// Issue #101: a browser now persists an opaque, server-issued token
// instead of the actual account password (see session/tokens.go). These
// exercise the three RPCs around it end to end, the same way a real
// login -> reload -> sign-out sequence would use them.
func TestIssueSessionTokenMintsATokenRedeemableOnAnotherConnection(t *testing.T) {
	ses := newTestAuthenticatedSession(t)
	ch := &connHandler{mg: &Manager{}}
	ch.setSession(ses)

	resp, closeConn := ch.processAuthRequest(&pb.ReqEnvelope{
		Id:      1,
		Payload: &pb.ReqEnvelope_ReqIssueSessionToken{ReqIssueSessionToken: &pb.ReqIssueSessionToken{}},
	})
	if closeConn {
		t.Error("expected closeConn to be false")
	}
	tokenResp, ok := resp.Payload.(*pb.RespEnvelope_RespSessionToken)
	if !ok {
		t.Fatalf("expected a RespSessionToken payload, got %T", resp.Payload)
	}
	token := tokenResp.RespSessionToken.SessionToken.Token
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if tokenResp.RespSessionToken.SessionToken.ExpiresAtUnixMs <= time.Now().UnixMilli() {
		t.Error("expected expiresAtUnixMs to be in the future")
	}

	// A separate connHandler stands in for the next page reload's own
	// fresh WebSocket connection.
	ch2 := &connHandler{mg: &Manager{}}
	authResp, closeConn2 := ch2.processNonAuthRequest(&pb.ReqEnvelope{
		Id:      2,
		Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: token}},
	})
	if closeConn2 {
		t.Error("expected closeConn to be false on a successful token redemption")
	}
	ack, ok := authResp.Payload.(*pb.RespEnvelope_RespAck)
	if !ok || !ack.RespAck.Ok {
		t.Fatalf("expected a successful RespAck, got %+v", authResp.Payload)
	}
	if ch2.getSession() != ses {
		t.Error("expected AuthWithToken to bind the exact original Session, not a new one")
	}
}

// An expired token is the ordinary case, not an attack, and the client's
// very next move is to fall back to the sign-in form over this same
// connection - so unlike a failed password Auth, this must leave the
// connection open. Closing it stranded the sign-in form's own
// GetPubKey/IsNewDevice requests waiting on a response that never came,
// and the app rendered an empty page (caught live on pit).
func TestAuthWithTokenRejectsUnknownTokenWithoutClosingTheConnection(t *testing.T) {
	ch := &connHandler{mg: &Manager{}}
	resp, closeConn := ch.processNonAuthRequest(&pb.ReqEnvelope{
		Id:      1,
		Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: "not-a-real-token"}},
	})
	if closeConn {
		t.Error("expected the connection to stay open so the client can fall back to signing in")
	}
	ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck)
	if !ok || ack.RespAck.Ok {
		t.Fatal("expected Ok=false for an unknown token")
	}
	if ch.getSession() != nil {
		t.Error("expected no session to be set after a failed token redemption")
	}
}

// A leaked token that's already been redeemed once must be worthless to
// whoever else might have a copy of it.
func TestAuthWithTokenIsSingleUse(t *testing.T) {
	ses := newTestAuthenticatedSession(t)
	token, _, err := session.IssueToken(ses)
	if err != nil {
		t.Fatalf("session.IssueToken: %v", err)
	}

	ch1 := &connHandler{mg: &Manager{}}
	resp1, _ := ch1.processNonAuthRequest(&pb.ReqEnvelope{
		Id:      1,
		Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: token}},
	})
	if ack := resp1.Payload.(*pb.RespEnvelope_RespAck); !ack.RespAck.Ok {
		t.Fatalf("expected the first redemption to succeed, got: %s", ack.RespAck.ErrorMsg)
	}

	ch2 := &connHandler{mg: &Manager{}}
	resp2, _ := ch2.processNonAuthRequest(&pb.ReqEnvelope{
		Id:      2,
		Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: token}},
	})
	if ack := resp2.Payload.(*pb.RespEnvelope_RespAck); ack.RespAck.Ok {
		t.Error("expected Ok=false on a repeated redemption of an already-used token")
	}
	if ch2.getSession() != nil {
		t.Error("expected no session to be set after redeeming an already-used token")
	}
}

func TestRevokeSessionTokenInvalidatesOutstandingTokens(t *testing.T) {
	ses := newTestAuthenticatedSession(t)
	token, _, err := session.IssueToken(ses)
	if err != nil {
		t.Fatalf("session.IssueToken: %v", err)
	}

	ch := &connHandler{mg: &Manager{}}
	ch.setSession(ses)
	resp, closeConn := ch.processAuthRequest(&pb.ReqEnvelope{
		Id:      1,
		Payload: &pb.ReqEnvelope_ReqRevokeSessionToken{ReqRevokeSessionToken: &pb.ReqRevokeSessionToken{}},
	})
	if closeConn {
		t.Error("expected closeConn to be false")
	}
	if ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck); !ok || !ack.RespAck.Ok {
		t.Fatalf("expected a successful RespAck, got %+v", resp.Payload)
	}

	ch2 := &connHandler{mg: &Manager{}}
	resp2, _ := ch2.processNonAuthRequest(&pb.ReqEnvelope{
		Id:      2,
		Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: token}},
	})
	if ack := resp2.Payload.(*pb.RespEnvelope_RespAck); ack.RespAck.Ok {
		t.Error("expected a revoked token to no longer redeem successfully")
	}
}

// Issue #85: the setup wizard's storage/WiFi steps configure the shared
// physical machine, so they belong to the primary instance alone. Hiding
// them from an additional user's wizard is the visible half of that; this
// is the half that actually holds, since these are ordinary authenticated
// RPCs any signed-in sub-user could send directly - and ReqSetupStorage
// wipes the selected disks while ReqSetWifi drops the machine's network.
func TestMachineLevelRPCsAreRefusedOnAChildInstance(t *testing.T) {
	cases := []struct {
		name string
		env  *pb.ReqEnvelope
	}{
		{"setup storage", &pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqSetupStorage{
			ReqSetupStorage: &pb.SetupStorage{DevicePaths: []string{"/dev/sda"}}}}},
		{"set wifi", &pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqSetWifi{
			ReqSetWifi: &pb.SetWifi{Ssid: "somewhere", Password: "hunter2"}}}},
		{"list storage devices", &pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqListStorageDevices{
			ReqListStorageDevices: &pb.ListStorageDevices{}}}},
		{"list wifi networks", &pb.ReqEnvelope{Id: 4, Payload: &pb.ReqEnvelope_ReqListWifiNetworks{
			ReqListWifiNetworks: &pb.ListWifiNetworks{}}}},
	}

	for _, c := range cases {
		// sup == nil is what makes this a child instance (see the
		// supervisor package's doc comment).
		ch := &connHandler{mg: &Manager{}}

		resp, _ := ch.processAuthRequest(c.env)

		if resp == nil {
			t.Fatalf("%s: expected a response", c.name)
		}
		if !resp.Error {
			t.Errorf("%s: expected the request to be refused on a child instance", c.name)
		}
		if resp.ErrorMessage != "not available on this instance" {
			t.Errorf("%s: ErrorMessage = %q, want the not-available message", c.name, resp.ErrorMessage)
		}
	}
}

// The same requests must still work on the primary - the guard is about
// which instance is asking, not about disabling setup altogether. A nil
// supervisor is the only thing that marks a child, so a non-nil one has
// to get past the guard (it fails later, on the real hardware calls this
// test has no business making - all that matters here is that it is not
// refused with the child-instance message).
func TestMachineLevelRPCsAreNotRefusedOnThePrimary(t *testing.T) {
	ch := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}
	env := &pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqListStorageDevices{
		ReqListStorageDevices: &pb.ListStorageDevices{}}}

	resp, _ := ch.processAuthRequest(env)

	if resp != nil && resp.ErrorMessage == "not available on this instance" {
		t.Error("the primary instance must not be refused its own storage/WiFi setup")
	}
}
