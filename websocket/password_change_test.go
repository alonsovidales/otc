// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/mediastream"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/supervisor"
)

// keep records an argument of a statement, to answer a later query with it.
type keep struct{ v *[]byte }

func (k keep) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	*k.v = append([]byte(nil), b...)
	return ok
}

// sessionWithVault is a real session signed in with password, on a mocked
// vault that later queries can be answered from (salt, secret).
func sessionWithVault(t *testing.T, password string) (ses *session.Session, mock sqlmock.Sqlmock, salt, secret *[]byte) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	salt, secret = new([]byte), new([]byte)
	mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	mock.ExpectExec("insert into `vault`").WithArgs(keep{secret}, keep{salt}).WillReturnResult(sqlmock.NewResult(1, 1))
	ses, err = session.New("owner-uuid", password, true, dao.NewWithDB(db))
	if err != nil {
		t.Fatal(err)
	}
	return ses, mock, salt, secret
}

// sealer is a connection's key, and what seals a password for it.
func sealer(t *testing.T) (*rsa.PrivateKey, func(string) []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return priv, func(s string) []byte {
		c, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &priv.PublicKey, []byte(s), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
}

func changeKey(ch *connHandler, seal func(string) []byte, oldKey, newKey string) *pb.RespEnvelope {
	resp, _ := ch.processAuthRequest(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqChangeKey{
		ReqChangeKey: &pb.ChangeKey{OldKey: seal(oldKey), NewKey: seal(newKey)}}}, "")
	return resp
}

// freshAttempts gives the test a password-attempt limiter of its own: the
// device-wide limit would otherwise carry its failures into other tests.
func freshAttempts(t *testing.T) {
	t.Helper()
	saved := session.Attempts
	session.Attempts = session.NewAuthLimiter()
	t.Cleanup(func() { session.Attempts = saved })
}

// tracked is a connection being served by mg, and whether it was closed.
func tracked(mg *Manager) (*connHandler, *bool) {
	ch := &connHandler{mg: mg}
	closed := new(bool)
	mg.track(ch, func() { *closed = true })
	return ch, closed
}

// ChangeKey's old password is a password check like a sign-in's: counted
// for the address, delayed when wrong, refused while locked out - a stolen
// session could otherwise try one guess after another.
func TestChangeKeyCountsWrongOldPasswords(t *testing.T) {
	saved := cFailedPasswordDelay
	cFailedPasswordDelay = time.Millisecond
	t.Cleanup(func() { cFailedPasswordDelay = saved })
	const addr = "198.51.100.40"
	freshAttempts(t)

	ses, mock, salt, secret := sessionWithVault(t, "right-password")
	priv, seal := sealer(t)
	ch := &connHandler{mg: &Manager{}, privKey: priv, remoteAddr: addr}
	ch.setSession(ses)

	for i := 1; i <= session.MaxAuthAttempts; i++ {
		mock.ExpectQuery("select `salt` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"salt"}).AddRow(*salt))
		mock.ExpectQuery("select `secret` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"secret"}).AddRow(*secret))
		resp := changeKey(ch, seal, "a-wrong-guess", "new-password-1")
		ack := resp.GetRespAck()
		if i < session.MaxAuthAttempts {
			if !resp.Error || ack != nil {
				t.Fatalf("guess %d: want the usual error, got %v", i, resp)
			}
			continue
		}
		if ack == nil || ack.Ok || ack.Code != "too_many_attempts" || ack.RetryAfterSeconds <= 0 {
			t.Fatalf("guess %d: want the lockout, got %v", i, resp)
		}
	}
	// Locked out: refused before the vault is read (no query expected).
	ack := changeKey(ch, seal, "right-password", "new-password-1").GetRespAck()
	if ack == nil || ack.Code != "too_many_attempts" {
		t.Fatalf("while locked out: got %v", ack)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A changed password signs out every other connection signed in with the
// old one and ends every session token; the one that changed it carries on
// (and gets a token), a friend's connection and a stranger's stay.
func TestChangeKeySignsOutTheOtherConnections(t *testing.T) {
	const addr = "198.51.100.41"
	freshAttempts(t)
	ses, mock, salt, secret := sessionWithVault(t, "right-password")
	priv, seal := sealer(t)
	mg := &Manager{}

	changer, changerClosed := tracked(mg)
	changer.privKey, changer.remoteAddr = priv, addr
	changer.setSession(ses)
	tab, tabClosed := tracked(mg)
	tab.setSession(ses) // another tab, redeemed from the same sign-in
	phone, phoneClosed := tracked(mg)
	phone.setSession(&session.Session{})
	friend, friendClosed := tracked(mg)
	friend.setFriendProfile(&profile.Profile{})
	_, strangerClosed := tracked(mg)

	oldToken, _, err := session.IssueToken(ses, session.PasswordGeneration())
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("select `salt` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"salt"}).AddRow(*salt))
	mock.ExpectQuery("select `secret` from `vault`").WillReturnRows(sqlmock.NewRows([]string{"secret"}).AddRow(*secret))
	mock.ExpectExec("update `vault` set `secret` = \\?, `salt` = \\?").WillReturnResult(sqlmock.NewResult(0, 1))

	if ack := changeKey(changer, seal, "right-password", "new-password-1").GetRespAck(); ack == nil || !ack.Ok {
		t.Fatalf("the change: got %v", ack)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if *changerClosed || changer.getSession() != ses {
		t.Error("the connection that changed the password was signed out")
	}
	if !*tabClosed || !*phoneClosed {
		t.Errorf("signed in with the old password: closed %v and %v, want both", *tabClosed, *phoneClosed)
	}
	if *friendClosed || *strangerClosed {
		t.Error("a friend's or a stranger's connection was closed")
	}
	if _, _, ok := session.RedeemToken(oldToken); ok {
		t.Error("a token from before the change still redeems")
	}
	resp, _ := changer.processAuthRequest(&pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqIssueSessionToken{ReqIssueSessionToken: &pb.ReqIssueSessionToken{}}}, "")
	if resp.GetRespSessionToken().GetSessionToken().GetToken() == "" {
		t.Errorf("the connection that changed it got no token: %v", resp)
	}
	// What the closed tab had already read still runs, but gets no token.
	resp, _ = tab.processAuthRequest(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqIssueSessionToken{ReqIssueSessionToken: &pb.ReqIssueSessionToken{}}}, "")
	if !resp.Error {
		t.Errorf("a request still running on a connection closed got a token: %v", resp)
	}
}

// A sign-in whose password (or token) was checked before a change and that
// ends after it signs nothing in: it could have been the old password.
func TestSignInRacingAPasswordChangeIsRefused(t *testing.T) {
	mg := &Manager{}
	ch, _ := tracked(mg)
	gen := session.PasswordGeneration()
	by, _ := tracked(mg)
	mg.passwordChanged(by)
	if ch.signIn(&session.Session{}, gen) || ch.getSession() != nil {
		t.Error("a sign-in checked before the change was taken")
	}
	if !ch.signIn(&session.Session{}, session.PasswordGeneration()) || ch.getSession() == nil {
		t.Error("a sign-in after the change was refused")
	}
}

// The demo device's password is public, so anyone can ask to stream a
// library video again and again: asked within the reuse window, the same
// file keeps its token instead of adding one each time.
func TestLibraryStreamsReuseTheirToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mg := &Manager{dao: dao.NewWithDB(db), media: mediastream.NewServer(mediastream.NewStore(), t.TempDir(), t.TempDir())}
	ch := &connHandler{mg: mg}
	ch.setSession(&session.Session{})
	row := func(hash string) {
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/v.mp4").
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
				AddRow(hash, "video/mp4", time.Now(), time.Now(), "/v.mp4", int64(64<<20)))
	}
	url := func() string {
		t.Helper()
		u, _, _, _, err := ch.issueMediaURL(&pb.ReqGetMediaURL{Path: "/v.mp4"})
		if err != nil || u == "" {
			t.Fatalf("got %q, %v", u, err)
		}
		return u
	}
	row(strings.Repeat("a", 64))
	first := url()
	row(strings.Repeat("a", 64))
	if again := url(); again != first {
		t.Errorf("the same file asked again got another token: %s, then %s", first, again)
	}
	row(strings.Repeat("b", 64)) // the path holds other content now
	if other := url(); other == first {
		t.Error("other content at the same path kept the old token")
	}
}

// The bridge account's password and the Tailscale auth key come sealed for
// this connection, as a sign-in's password does (the bridge relays them);
// an older client's plain field is still read when no sealed one comes.
func TestSealedSecretsAreOpenedWithTheConnectionsKey(t *testing.T) {
	priv, seal := sealer(t)
	ch := &connHandler{mg: &Manager{}, privKey: priv}
	if got, err := ch.sealedOr(seal("account-password"), "ignored"); err != nil || got != "account-password" {
		t.Errorf("sealed: %q, %v", got, err)
	}
	if got, err := ch.sealedOr(nil, "an older web app's"); err != nil || got != "an older web app's" {
		t.Errorf("plain: %q, %v", got, err)
	}
	if _, err := ch.sealedOr([]byte("not sealed for this connection"), "plain"); err == nil {
		t.Error("a value this connection's key can't open was taken")
	}

	// One it can't open is refused before anything runs with it.
	primary := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}, privKey: priv}
	primary.setSession(&session.Session{})
	resp, _ := primary.processAuthRequest(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqSetupTailscale{
		ReqSetupTailscale: &pb.ReqSetupTailscale{Enable: true, AuthKeySealed: []byte("garbage")}}}, "")
	if !resp.Error || resp.GetRespTailscaleStatus() != nil {
		t.Errorf("an auth key that can't be opened: %v", resp)
	}
}
