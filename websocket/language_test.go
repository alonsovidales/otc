// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/alonsovidales/otc/settings"
	"github.com/alonsovidales/otc/updater"
	"google.golang.org/protobuf/proto"
)

// languageManager is a Manager whose settings come from a mocked database
// storing language and lastUI.
func languageManager(t *testing.T, language, lastUI string) (*Manager, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mock.ExpectQuery("select `subdomain`, `device_uuid`, `bridge_secret` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"subdomain", "device_uuid", "bridge_secret"}).AddRow("me.off-the.cloud", "dev-uuid", "bridge-secret"))
	mock.ExpectQuery("select `face_recognition_enabled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"face_recognition_enabled"}).AddRow(false))
	mock.ExpectQuery("select `image_tagging_enabled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"image_tagging_enabled"}).AddRow(true))
	mock.ExpectQuery("select `language`, `last_ui_language` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"language", "last_ui_language"}).AddRow(language, lastUI))
	d := dao.NewWithDB(db)
	st, err := settings.Init(d)
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{dao: d, settings: st}, mock
}

func setLanguageReq(id int32, language string, expected *string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: id, Payload: &pb.ReqEnvelope_ReqSetLanguage{
		ReqSetLanguage: &pb.SetLanguage{Language: language, Expected: expected},
	}}
}

func getSettingsReq() *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqGetSettings{ReqGetSettings: &pb.GetSettings{}}}
}

func ackOf(t *testing.T, resp *pb.RespEnvelope) *pb.Ack {
	t.Helper()
	if resp == nil || resp.Error {
		t.Fatalf("expected an Ack, got %+v", resp)
	}
	ack := resp.GetRespAck()
	if ack == nil {
		t.Fatalf("expected an Ack, got %T", resp.Payload)
	}
	return ack
}

// settingsLanguage asks GetSettings and returns its language, which is
// always present.
func settingsLanguage(t *testing.T, ch *connHandler, mock sqlmock.Sqlmock) string {
	t.Helper()
	mock.ExpectQuery("select `social_storage_limit_mb` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"social_storage_limit_mb"}).AddRow(5120))
	mock.ExpectQuery("select sum\\(`size`\\)").WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(nil))
	resp, _ := ch.processMessage(getSettingsReq())
	s := resp.GetRespSettings()
	if s == nil || s.Language == nil {
		t.Fatalf("expected Settings with a language, got %+v", resp)
	}
	return *s.Language
}

// SetLanguage is the owner's: before sign-in and from a friend's device it
// is not answered (serveConnection sends not_authenticated) and nothing is
// written.
func TestSetLanguageIsOwnerOnly(t *testing.T) {
	mg, mock := languageManager(t, "", "")

	resp, _ := (&connHandler{mg: mg}).processMessage(setLanguageReq(1, "es", nil))
	if resp != nil {
		t.Errorf("before sign-in: %+v", resp)
	}

	mock.ExpectQuery("select `status`, `forget_requested` is not null from `social_friendship`").
		WillReturnRows(sqlmock.NewRows([]string{"status", "leaving"}).AddRow("accepted", false))
	friend := &connHandler{mg: mg}
	friend.setFriendProfile(&profile.Profile{})
	resp, _ = friend.processMessage(setLanguageReq(2, "es", nil))
	if resp != nil {
		t.Errorf("a friend: %+v", resp)
	}

	if mg.settings.Language() != "" {
		t.Errorf("stored %q", mg.settings.Language())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// SetLanguage validates the code, applies expected as a compare-and-set,
// and Settings carries the stored value from then on, "" included.
func TestSetLanguageAnswers(t *testing.T) {
	mg, mock := languageManager(t, "", "")
	ch := &connHandler{mg: mg}
	ch.setSession(newTestAuthenticatedSession(t))

	if got := settingsLanguage(t, ch, mock); got != "" {
		t.Errorf("a new device: %q", got)
	}

	for _, bad := range []string{"ES", "spanish", "es-ES", "e", "<b>"} {
		ack := ackOf(t, mustProcess(t, ch, setLanguageReq(4, bad, nil)))
		if ack.Ok || ack.Code != cCodeInvalidLanguage {
			t.Errorf("%q: %+v", bad, ack)
		}
	}

	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("es").WillReturnResult(sqlmock.NewResult(0, 1))
	if ack := ackOf(t, mustProcess(t, ch, setLanguageReq(5, "es", proto.String("")))); !ack.Ok || ack.Code != "" {
		t.Errorf("first change: %+v", ack)
	}
	if got := settingsLanguage(t, ch, mock); got != "es" {
		t.Errorf("after the change: %q", got)
	}

	// Another app, which last saw Automatic: told it changed, nothing written.
	if ack := ackOf(t, mustProcess(t, ch, setLanguageReq(6, "fr", proto.String("")))); ack.Ok || ack.Code != cCodeLanguageChanged {
		t.Errorf("stale expected: %+v", ack)
	}
	// Without expected (an app that never saw a value): written.
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("fr").WillReturnResult(sqlmock.NewResult(0, 1))
	if ack := ackOf(t, mustProcess(t, ch, setLanguageReq(7, "fr", nil))); !ack.Ok {
		t.Errorf("no expected: %+v", ack)
	}
	// A language this build has no text for is kept for the apps that do.
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("ja").WillReturnResult(sqlmock.NewResult(0, 1))
	if ack := ackOf(t, mustProcess(t, ch, setLanguageReq(8, "ja", proto.String("fr")))); !ack.Ok {
		t.Errorf("unknown code: %+v", ack)
	}
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("").WillReturnResult(sqlmock.NewResult(0, 1))
	if ack := ackOf(t, mustProcess(t, ch, setLanguageReq(9, "", proto.String("ja")))); !ack.Ok {
		t.Errorf("back to Automatic: %+v", ack)
	}
	if got := settingsLanguage(t, ch, mock); got != "" {
		t.Errorf("Automatic again: %q", got)
	}

	// A database failure is an error: the app keeps its change pending.
	mock.ExpectExec("update `settings` set `language` = \\?").WillReturnError(sqlmock.ErrCancelled)
	resp := mustProcess(t, ch, setLanguageReq(10, "de", nil))
	if !resp.Error || resp.Payload != nil {
		t.Errorf("failed write: %+v", resp)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func mustProcess(t *testing.T, ch *connHandler, env *pb.ReqEnvelope) *pb.RespEnvelope {
	t.Helper()
	resp, closeConn := ch.processMessage(env)
	if resp == nil || closeConn {
		t.Fatalf("no answer (close %v)", closeConn)
	}
	if resp.Id != env.Id {
		t.Fatalf("answer for %d, asked %d", resp.Id, env.Id)
	}
	return resp
}

// Two apps changing the language at the same moment from what they both
// saw (two connections, or two requests of one - they run concurrently):
// one wins, the other is told it changed and adopts it.
func TestSetLanguageConcurrentRequests(t *testing.T) {
	ses := newTestAuthenticatedSession(t)
	for round := 0; round < 20; round++ {
		mg, mock := languageManager(t, "", "")
		mock.ExpectExec("update `settings` set `language` = \\?").WillReturnResult(sqlmock.NewResult(0, 1))
		langs := []string{"es", "de"}
		acks := make([]*pb.Ack, len(langs))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range langs {
			ch := &connHandler{mg: mg}
			ch.setSession(ses)
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				resp, _ := ch.processMessage(setLanguageReq(int32(i+1), langs[i], proto.String("")))
				acks[i] = resp.GetRespAck()
			}(i)
		}
		close(start)
		wg.Wait()
		ok := 0
		for i, ack := range acks {
			switch {
			case ack == nil:
				t.Fatalf("round %d: request %d not answered with an Ack", round, i)
			case ack.Ok:
				ok++
				if mg.settings.Language() != langs[i] {
					t.Fatalf("round %d: %q won but %q is stored", round, langs[i], mg.settings.Language())
				}
			case ack.Code != cCodeLanguageChanged:
				t.Fatalf("round %d: %+v", round, ack)
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d stored", round, ok)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

// cStatusLanguageChildEnv marks the child process
// TestStatusCarriesTheLanguage runs in.
const cStatusLanguageChildEnv = "OTC_WS_STATUS_LANGUAGE_CHILD"

// Status carries the language, always present, so every app polling it
// picks up a change made in another.
func TestStatusCarriesTheLanguage(t *testing.T) {
	if os.Getenv(cStatusLanguageChildEnv) != "1" {
		// In a child process: GetStatus reads [otc] storage-path, and a
		// loaded config can't be unloaded (searchPhotosStorage).
		name := "TestStatusCarriesTheLanguage"
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), cStatusLanguageChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("in its own process: %v\n%s", err, out)
		}
		return
	}
	searchPhotosStorage(t)
	status := func(ch *connHandler) *string {
		t.Helper()
		resp := mustProcess(t, ch, &pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}}})
		st := resp.GetRespStatus()
		if st == nil {
			t.Fatalf("expected a Status, got %+v", resp)
		}
		return st.Language
	}

	mg, mock := languageManager(t, "", "")
	ch := &connHandler{mg: mg}
	ch.setSession(newTestAuthenticatedSession(t))
	if l := status(ch); l == nil || *l != "" {
		t.Errorf("Automatic: %v", l)
	}
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("nl").WillReturnResult(sqlmock.NewResult(0, 1))
	ackOf(t, mustProcess(t, ch, setLanguageReq(3, "nl", proto.String(""))))
	if l := status(ch); l == nil || *l != "nl" {
		t.Errorf("after SetLanguage: %v", l)
	}
	// Read from the database at start.
	mg2, _ := languageManager(t, "fr", "en")
	ch2 := &connHandler{mg: mg2}
	ch2.setSession(newTestAuthenticatedSession(t))
	if l := status(ch2); l == nil || *l != "fr" {
		t.Errorf("stored: %v", l)
	}
}

// requestLang: a language this build carries, by its base subtag, or ""
// for anything else - which replies render in today's English.
func TestRequestLang(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"en", "en"},
		{"EN-us", "en"},
		{"en_GB", "en"},
		{"xx", ""},
		{"x-pseudo-long", ""},
		{"<script>", ""},
		{"en\x00", ""},
	} {
		if got := requestLang(&pb.ReqEnvelope{Lang: c.in}); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
	if got := requestLang(nil); got != "" {
		t.Errorf("nil envelope: %q", got)
	}
}

// Phase 1 changes no reply: whatever lang a request carries, every reply
// is the one the same request gets without it, byte for byte.
func TestLangChangesNoReply(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	shortKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &priv.PublicKey, []byte("short"), nil)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &priv.PublicKey, []byte("test-password"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ses := newTestAuthenticatedSession(t)
	owner := func() *connHandler {
		ch := &connHandler{mg: &Manager{}, privKey: priv}
		ch.setSession(ses)
		return ch
	}
	anonymous := func() *connHandler { return &connHandler{mg: &Manager{}} }
	cases := []struct {
		name string
		ch   func() *connHandler
		env  func() *pb.ReqEnvelope
	}{
		{"unknown payload", owner, func() *pb.ReqEnvelope { return &pb.ReqEnvelope{Id: 1} }},
		{"bridge secret refused", owner, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqRegenerateBridgeSecret{ReqRegenerateBridgeSecret: &pb.RegenerateBridgeSecret{}}}
		}},
		{"short new password", owner, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqChangeKey{ReqChangeKey: &pb.ChangeKey{OldKey: oldKey, NewKey: shortKey}}}
		}},
		{"local endpoint not known yet", owner, func() *pb.ReqEnvelope { return localEndpointReq() }},
		{"friend domain too long", anonymous, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Id: 4, Payload: &pb.ReqEnvelope_ReqGetFriendshipStatus{
				ReqGetFriendshipStatus: &pb.GetFriendshipStatus{Domain: strings.Repeat("a", cMaxFriendDomain+1)}}}
		}},
	}
	marshal := proto.MarshalOptions{Deterministic: true}
	for _, c := range cases {
		resp, _ := c.ch().processMessage(c.env())
		want, err := marshal.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		for _, lang := range []string{"en", "EN-gb", "es", "fr-FR", "pt-BR", "qps", "xx", "<b>"} {
			env := c.env()
			env.Lang = lang
			resp, _ := c.ch().processMessage(env)
			got, err := marshal.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s with lang %q: got %+v, want the lang-less reply", c.name, lang, resp)
			}
		}
	}
}

// An owner's push registration records the language of the app that made
// it, when this build carries it and it changed - and the reply is the
// same either way. Through processMessage, so the request's lang is the
// one the handler sees.
func TestPushRegistrationNotesTheAppLanguage(t *testing.T) {
	regs := []struct {
		name string
		save string
		env  func(lang string) *pb.ReqEnvelope
	}{
		{"APNs", "insert into `apns_tokens`", func(lang string) *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Id: 1, Lang: lang, Payload: &pb.ReqEnvelope_ReqRegisterApnsToken{ReqRegisterApnsToken: &pb.RegisterApnsToken{Token: "tok"}}}
		}},
		{"FCM", "insert into `fcm_tokens`", func(lang string) *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Id: 2, Lang: lang, Payload: &pb.ReqEnvelope_ReqRegisterFcmToken{ReqRegisterFcmToken: &pb.RegisterFcmToken{Token: "tok"}}}
		}},
		{"Web Push", "insert into `web_push_subscriptions`", func(lang string) *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Id: 3, Lang: lang, Payload: &pb.ReqEnvelope_ReqRegisterWebPush{ReqRegisterWebPush: &pb.RegisterWebPush{Endpoint: "https://push.example/x", P256Dh: "k", Auth: "a"}}}
		}},
	}
	for _, r := range regs {
		t.Run(r.name, func(t *testing.T) {
			mg, mock := languageManager(t, "", "")
			ch := &connHandler{mg: mg}
			ch.setSession(newTestAuthenticatedSession(t))
			// register sends the registration with lang; write is what
			// last_ui_language is then expected to be written as ("" for
			// nothing), with err as that write's outcome.
			register := func(lang, write string, err error) {
				t.Helper()
				mock.ExpectExec(r.save).WillReturnResult(sqlmock.NewResult(0, 1))
				if write != "" {
					e := mock.ExpectExec("update `settings` set `last_ui_language` = \\?").WithArgs(write)
					if err != nil {
						e.WillReturnError(err)
					} else {
						e.WillReturnResult(sqlmock.NewResult(0, 1))
					}
				}
				resp := mustProcess(t, ch, r.env(lang))
				if ack := ackOf(t, resp); !ack.Ok || ack.Code != "" || ack.ErrorMsg != "" {
					t.Fatalf("lang %q: %+v", lang, ack)
				}
			}

			// No lang (a released app), or one this build doesn't carry:
			// nothing written.
			register("", "", nil)
			register("xx", "", nil)
			if mg.settings.LastUILanguage() != "" || mg.settings.PushLanguage() != "en" {
				t.Errorf("unwritten: %q", mg.settings.LastUILanguage())
			}
			// A failed write leaves the reply alone, and is tried again at
			// the next registration.
			register("en", "en", sqlmock.ErrCancelled)
			if mg.settings.LastUILanguage() != "" {
				t.Errorf("failed write: %q", mg.settings.LastUILanguage())
			}
			register("en-GB", "en", nil)
			if mg.settings.LastUILanguage() != "en" {
				t.Errorf("written: %q", mg.settings.LastUILanguage())
			}
			// Every launch re-registers: unchanged, not written again.
			register("en", "", nil)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}

	// From a friend's device the handlers aren't reached at all.
	mg, mock := languageManager(t, "", "")
	mock.ExpectQuery("select `status`, `forget_requested` is not null from `social_friendship`").
		WillReturnRows(sqlmock.NewRows([]string{"status", "leaving"}).AddRow("accepted", false))
	friend := &connHandler{mg: mg}
	friend.setFriendProfile(&profile.Profile{})
	if resp, _ := friend.processMessage(regs[0].env("en")); resp != nil {
		t.Errorf("a friend: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The registrations sent to the bridge carry the push language: the
// stored choice, else the last app's, else English.
func TestPushRegistrationsCarryTheLanguage(t *testing.T) {
	for _, c := range []struct{ stored, lastUI, want string }{
		{"", "", "en"},
		{"", "en", "en"},
		{"es", "en", "es"},
	} {
		mg, mock := languageManager(t, c.stored, c.lastUI)
		mock.ExpectQuery("select `token` from `apns_tokens`").WillReturnRows(sqlmock.NewRows([]string{"token"}).AddRow("a1"))
		mock.ExpectQuery("select `token` from `fcm_tokens`").WillReturnRows(sqlmock.NewRows([]string{"token"}).AddRow("f1"))
		mock.ExpectQuery("select `endpoint`, `p256dh`, `auth` from `web_push_subscriptions`").
			WillReturnRows(sqlmock.NewRows([]string{"endpoint", "p256dh", "auth"}).AddRow("https://push.example/x", "k", "a"))
		mock.ExpectQuery("select `vapid_public_key`, `vapid_private_key` from `settings`").
			WillReturnRows(sqlmock.NewRows([]string{"vapid_public_key", "vapid_private_key"}).AddRow("pub", "priv"))
		regs, ok := mg.pushRegistrations()
		if !ok {
			t.Fatalf("%+v: not built", c)
		}
		if regs.Language != c.want {
			t.Errorf("%+v: language %q", c, regs.Language)
		}
		if regs.Domain != "me.off-the.cloud" || regs.OwnerUuid != "dev-uuid" || regs.Secret != "bridge-secret" ||
			len(regs.ApnsTokens) != 1 || len(regs.FcmTokens) != 1 || len(regs.WebPushSubs) != 1 || regs.VapidPrivateKey != "priv" {
			t.Errorf("%+v: the rest of the set: %+v", c, regs)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	}
}

const updateAlertCount = "select count\\(\\*\\) from `notifications` where `type` = 'Update' and \\(`update_version` = \\? or `title` in \\(\\?, \\?\\)\\)"

// An update alert is added once per version, matched by update_version
// and by both English titles (rows written before release 118 have only
// a title), and a critical one is pushed only when its alert was added.
func TestUpdateAlertOncePerVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)
	var pushed []string
	notify := func(title, body string, _ push.Target) { pushed = append(pushed, title+" | "+body) }
	critical := &updater.Alert{Level: updater.KindCritical, Version: "3.0", Target: 120, Summary: "A security fix."}
	const major3, critical3 = "Update 3.0 is available", "Critical update 3.0 - please install it soon"

	mock.ExpectQuery(updateAlertCount).WithArgs("3.0", major3, critical3).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `notifications`.*`update_version`").
		WithArgs(sqlmock.AnyArg(), critical3, "A security fix. Install it from Settings.", "3.0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	updateAlert(d, notify, critical)
	if len(pushed) != 1 || pushed[0] != critical3+" | A security fix. Install it from Settings." {
		t.Errorf("first time: pushed %q", pushed)
	}

	// Already alerted (after a restart, or by a row from before 118 with
	// the major title): no row, no push.
	mock.ExpectQuery(updateAlertCount).WithArgs("3.0", major3, critical3).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	updateAlert(d, notify, critical)
	if len(pushed) != 1 {
		t.Errorf("again: pushed %q", pushed)
	}

	// A major one is added, never pushed.
	mock.ExpectQuery(updateAlertCount).WithArgs("3.1", "Update 3.1 is available", "Critical update 3.1 - please install it soon").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `notifications`.*`update_version`").
		WithArgs(sqlmock.AnyArg(), "Update 3.1 is available", "More. Install it from Settings.", "3.1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	updateAlert(d, notify, &updater.Alert{Level: updater.KindMajor, Version: "3.1", Target: 121, Summary: "More."})
	if len(pushed) != 1 {
		t.Errorf("major: pushed %q", pushed)
	}

	// A database failure: logged, nothing pushed.
	mock.ExpectQuery(updateAlertCount).WillReturnError(sqlmock.ErrCancelled)
	updateAlert(d, notify, &updater.Alert{Level: updater.KindCritical, Version: "4.0", Target: 130, Summary: "x"})
	if len(pushed) != 1 {
		t.Errorf("failed: pushed %q", pushed)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
