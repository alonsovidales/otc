// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// langDevice keeps a user's language as the device does: stored is nil on
// a device that predates localization (SetLanguage is unknown_payload
// there and its status has no language), and a change is kept only while
// the stored value equals expected (when sent), else Ack code "changed".
type langDevice struct {
	mu     sync.Mutex
	stored *string
	sets   []*pb.SetLanguage
	langs  []string // ReqEnvelope.lang of each request
	fail   bool     // answer SetLanguage with an error
	hold   chan struct{}
}

func strp(s string) *string { return &s }

func (d *langDevice) handle(req *pb.ReqEnvelope, pubDER []byte) *pb.RespEnvelope {
	d.mu.Lock()
	d.langs = append(d.langs, req.Lang)
	hold := d.hold
	d.mu.Unlock()
	resp := &pb.RespEnvelope{Id: req.Id}
	switch p := req.Payload.(type) {
	case *pb.ReqEnvelope_ReqGetPubKey:
		resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
	case *pb.ReqEnvelope_ReqAuth:
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
	case *pb.ReqEnvelope_ReqGetStatus:
		d.mu.Lock()
		st := &pb.Status{}
		if d.stored != nil {
			st.Language = strp(*d.stored)
		}
		d.mu.Unlock()
		resp.Payload = &pb.RespEnvelope_RespStatus{RespStatus: st}
	case *pb.ReqEnvelope_ReqSetLanguage:
		if hold != nil {
			<-hold
		}
		d.mu.Lock()
		d.sets = append(d.sets, p.ReqSetLanguage)
		switch set := p.ReqSetLanguage; {
		case d.stored == nil:
			resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "unknown_payload", "This device does not understand that request"
		case d.fail:
			resp.Error, resp.ErrorMessage = true, "database is busy"
		case set.Expected != nil && *set.Expected != *d.stored:
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: false, Code: "changed", ErrorMsg: "changed meanwhile"}}
		default:
			d.stored = strp(set.Language)
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		}
		d.mu.Unlock()
	default:
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
	}

	return resp
}

// sent is what the device was asked, as "language/expected" ("-" for no
// expected).
func (d *langDevice) sent() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, s := range d.sets {
		exp := "-"
		if s.Expected != nil {
			exp = *s.Expected
		}
		out = append(out, s.Language+"/"+exp)
	}

	return out
}

// langFixture: an engine over cfg (saved as config.json) signed in to d.
func langFixture(t *testing.T, d *langDevice, cfg *config.Config) *Engine {
	t.Helper()
	withConfigDir(t)
	cfg.Domain = "dev-a"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var wmu sync.Mutex
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			req := &pb.ReqEnvelope{}
			if proto.Unmarshal(data, req) != nil {
				return
			}
			// Each on its own, as the device does: a held SetLanguage
			// doesn't hold up the status.
			go func() {
				b, _ := proto.Marshal(d.handle(req, pubDER))
				wmu.Lock()
				defer wmu.Unlock()
				_ = c.WriteMessage(websocket.BinaryMessage, b)
			}()
		}
	}))
	t.Cleanup(srv.Close)
	ws := wsclient.New()
	connected := make(chan struct{}, 1)
	ws.OnConnect = func() { connected <- struct{}{} }
	ws.Configure("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "test", "secret")
	ws.Connect()
	t.Cleanup(ws.Disconnect)
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("never connected")
	}
	e := New(cfg, "", nil)
	e.mu.Lock()
	e.ws = ws
	e.applyLanguageLocked()
	e.mu.Unlock()
	t.Cleanup(func() {
		e.mu.Lock()
		e.stopped = true
		e.mu.Unlock()
	})

	return e
}

// settle waits for the SetLanguage under way, and those it leads to.
func settle(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for quiet := 0; quiet < 3; {
		e.mu.Lock()
		busy := e.langSending
		e.mu.Unlock()
		if busy {
			quiet = 0
		} else {
			quiet++
		}
		if time.Now().After(deadline) {
			t.Fatal("a SetLanguage never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func langOnDisk(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}

func eqStrings(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// A change made here (the tray, `otc-sync language`) goes at connect,
// against the value it was made against, before the status poll; once
// acknowledged it is cleared in config.json and never sent again.
func TestLanguageChangeSentAtConnectThenCleared(t *testing.T) {
	d := &langDevice{stored: strp("")}
	cfg := &config.Config{}
	cfg.SetLanguage("es", strp(""), time.Now().Add(-time.Hour)) // made offline, long ago
	e := langFixture(t, d, cfg)

	e.languageAtConnect()
	e.pollRaid()
	settle(t, e)

	if got := d.sent(); !eqStrings(got, []string{"es/"}) {
		t.Fatalf("asked %v", got)
	}
	if disk := langOnDisk(t); disk.Language != "es" || disk.LanguagePending != nil || disk.LanguageExpected != nil {
		t.Fatalf("config.json: %+v", disk)
	}
	if st := e.Snapshot(); st.DeviceLanguage == nil || *st.DeviceLanguage != "es" || st.LanguageUnsupported {
		t.Fatalf("snapshot: %v %v", st.DeviceLanguage, st.LanguageUnsupported)
	}
	e.languageAtConnect()
	e.pollRaid()
	settle(t, e)
	if got := d.sent(); len(got) != 1 {
		t.Fatalf("sent again: %v", got)
	}
}

// Without anything pending the device's value is adopted - written to
// config.json, and every request carries the language it gives.
func TestLanguageAdoptedFromTheDevice(t *testing.T) {
	d := &langDevice{stored: strp("fr")}
	e := langFixture(t, d, &config.Config{Language: "de"})

	e.pollRaid()

	if disk := langOnDisk(t); disk.Language != "fr" || disk.LanguagePending != nil {
		t.Fatalf("config.json: %+v", disk)
	}
	d.mu.Lock()
	d.langs = nil
	d.mu.Unlock()
	e.pollRaid()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.langs) != 1 || d.langs[0] != oslang.Effective("fr") {
		t.Fatalf("requests went with %q, want %q", d.langs, oslang.Effective("fr"))
	}
	if len(d.sets) != 0 {
		t.Fatalf("an adopted value is not sent back: %v", d.sets)
	}
}

// A pending change outweighs a different value for 30 s (a status from
// before the change), and then the device's value is adopted; the
// device's reporting the change itself clears it.
func TestLanguagePendingIgnoresTheDeviceForAWhile(t *testing.T) {
	d := &langDevice{stored: strp(""), fail: true}
	cfg := &config.Config{}
	cfg.SetLanguage("es", strp(""), time.Now())
	e := langFixture(t, d, cfg)

	e.languageAtConnect() // refused: stays pending
	settle(t, e)
	e.pollRaid()
	if disk := langOnDisk(t); disk.Language != "es" || disk.LanguagePending == nil {
		t.Fatalf("adopted within the window: %+v", disk)
	}

	// The device has it after all (another app sent the same).
	d.mu.Lock()
	d.stored = strp("es")
	d.mu.Unlock()
	e.pollRaid()
	if disk := langOnDisk(t); disk.Language != "es" || disk.LanguagePending != nil {
		t.Fatalf("its own value seen: %+v", disk)
	}

	// Pending again, refused, and past the window: the device's wins.
	disk := langOnDisk(t)
	disk.SetLanguage("nl", strp("es"), time.Now().Add(-languageWindow-time.Second))
	if err := disk.Save(); err != nil {
		t.Fatal(err)
	}
	e.UpdateConfig(disk, "")
	settle(t, e)
	e.pollRaid()
	if disk := langOnDisk(t); disk.Language != "es" || disk.LanguagePending != nil {
		t.Fatalf("after the window: %+v", disk)
	}
}

// "changed": another app changed it since the value this change was made
// against - that one is adopted, nothing overwritten.
func TestLanguageChangedElsewhereIsAdopted(t *testing.T) {
	d := &langDevice{stored: strp("it")}
	cfg := &config.Config{}
	cfg.SetLanguage("es", strp(""), time.Now())
	e := langFixture(t, d, cfg)

	e.languageAtConnect()
	settle(t, e)

	if disk := langOnDisk(t); disk.Language != "it" || disk.LanguagePending != nil {
		t.Fatalf("config.json: %+v", disk)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if *d.stored != "it" {
		t.Fatalf("overwritten: %q", *d.stored)
	}
}

// A device that predates localization keeps nothing: the choice stays
// here, still pending, nothing more is sent until the next connection,
// and its status (without a language) leaves the copy alone.
func TestLanguageOnAnOldDevice(t *testing.T) {
	d := &langDevice{}
	cfg := &config.Config{}
	cfg.SetLanguage("pt", nil, time.Now())
	e := langFixture(t, d, cfg)

	e.languageAtConnect()
	settle(t, e)
	e.pollRaid()

	if disk := langOnDisk(t); disk.Language != "pt" || disk.LanguagePending == nil {
		t.Fatalf("config.json: %+v", disk)
	}
	if st := e.Snapshot(); !st.LanguageUnsupported || st.DeviceLanguage != nil {
		t.Fatalf("snapshot: %v %v", st.LanguageUnsupported, st.DeviceLanguage)
	}
	// Chosen again meanwhile: kept here, not sent to a device that can't.
	disk := langOnDisk(t)
	disk.SetLanguage("nl", nil, time.Now())
	if err := disk.Save(); err != nil {
		t.Fatal(err)
	}
	e.UpdateConfig(disk, "")
	settle(t, e)
	if got := d.sent(); !eqStrings(got, []string{"pt/-"}) {
		t.Fatalf("asked %v", got)
	}
	// The next connection asks again, and a device updated meanwhile
	// keeps it.
	d.mu.Lock()
	d.stored = strp("")
	d.mu.Unlock()
	e.languageAtConnect()
	settle(t, e)
	if got := d.sent(); !eqStrings(got, []string{"pt/-", "nl/-"}) {
		t.Fatalf("after reconnecting: %v", got)
	}
	e.pollRaid()
	if disk := langOnDisk(t); disk.Language != "nl" || disk.LanguagePending != nil || *d.stored != "nl" {
		t.Fatalf("config.json: %+v; the device: %q", disk, *d.stored)
	}
}

// A change made while one is on its way goes right after it, against the
// value the device was just given - not taken for one made elsewhere.
func TestLanguageChangedWhileSending(t *testing.T) {
	d := &langDevice{stored: strp(""), hold: make(chan struct{})}
	cfg := &config.Config{}
	cfg.SetLanguage("es", strp(""), time.Now())
	e := langFixture(t, d, cfg)

	e.languageAtConnect()
	e.pollRaid() // answered while es is on its way: "" must not be adopted
	disk := langOnDisk(t)
	if disk.Language != "es" {
		t.Fatalf("adopted while sending: %+v", disk)
	}
	disk.SetLanguage("fr", strp(""), time.Now())
	if err := disk.Save(); err != nil {
		t.Fatal(err)
	}
	e.UpdateConfig(disk, "")
	close(d.hold)
	settle(t, e)

	if got := d.sent(); !eqStrings(got, []string{"es/", "fr/es"}) {
		t.Fatalf("asked %v", got)
	}
	if disk := langOnDisk(t); disk.Language != "fr" || disk.LanguagePending != nil {
		t.Fatalf("config.json: %+v", disk)
	}
}

// Another device or password: what the old one said doesn't count, and
// a change made against its value isn't sent to the new one. The choice
// stays.
func TestLanguageForgottenWithTheDevice(t *testing.T) {
	d := &langDevice{stored: strp("es")}
	e := langFixture(t, d, &config.Config{Language: "es"})
	e.pollRaid()
	if st := e.Snapshot(); st.DeviceLanguage == nil {
		t.Fatal("not seen")
	}
	other := langOnDisk(t)
	other.SetLanguage("de", strp("es"), time.Now()) // never reached dev-a
	other.Domain = "dev-b"
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	e.UpdateConfig(other, "") // no password: nothing is dialled
	if st := e.Snapshot(); st.DeviceLanguage != nil {
		t.Fatalf("kept across devices: %v", st.DeviceLanguage)
	}
	disk := langOnDisk(t)
	if disk.Language != "de" || disk.LanguagePending != nil || disk.LanguageExpected != nil {
		t.Fatalf("config.json: %+v", disk)
	}
	e.mu.Lock()
	pending := e.cfg.LanguagePending
	e.mu.Unlock()
	if pending != nil {
		t.Fatal("still pending in memory")
	}
}
