// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/settings"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// The contract with the apps (issue #207): while the storage is missing,
// every request - GetPubKey first - is an error with the code and the
// device's words in the request's language, no payload; only the
// bridge's fetch of the web app's files is served.
func TestUnavailableAnswersEverythingStorageUnavailable(t *testing.T) {
	static := t.TempDir()
	os.MkdirAll(filepath.Join(static, "assets"), 0o755)
	os.WriteFile(filepath.Join(static, "index.html"), []byte("<html>app</html>"), 0o644)
	os.WriteFile(filepath.Join(static, "assets", "index-abcd1234.js"), []byte("console.log(1)"), 0o644)
	ch := &connHandler{mg: &Manager{unavailable: true, staticPath: static + "/"}}

	for _, c := range []struct {
		lang string
		env  *pb.ReqEnvelope
	}{
		{"", &pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}}},
		{"es", &pb.ReqEnvelope{Id: 2, Lang: "es", Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}}},
		{"fr", &pb.ReqEnvelope{Id: 3, Lang: "fr", Payload: &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}}}},
		{"", &pb.ReqEnvelope{Id: 4, Payload: &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{}}}},
		{"", &pb.ReqEnvelope{Id: 5, Payload: &pb.ReqEnvelope_ReqBridgeClientInfo{ReqBridgeClientInfo: &pb.BridgeClientInfo{RemoteAddr: "1.2.3.4"}}}},
	} {
		resp := mustProcess(t, ch, c.env)
		if !resp.Error || resp.ErrorCode != "storage_unavailable" || resp.Payload != nil {
			t.Errorf("%d: %+v", c.env.Id, resp)
		}
		if want := i18n.DevErrStorageUnavailable().Render(c.lang); resp.ErrorMessage != want {
			t.Errorf("%d: %q, want %q", c.env.Id, resp.ErrorMessage, want)
		}
	}
	// The English the apps keep as their fallback, word for word.
	if got := mustProcess(t, ch, &pb.ReqEnvelope{Id: 6, Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}}).ErrorMessage; got !=
		"Your device can't reach its storage disks. Check they're plugged in and switched on; it starts again by itself once they're back." {
		t.Errorf("English: %q", got)
	}
	resp := mustProcess(t, ch, &pb.ReqEnvelope{Id: 7, Payload: &pb.ReqEnvelope_ReqGetStaticAsset{ReqGetStaticAsset: &pb.ReqGetStaticAsset{Path: "/assets/index-abcd1234.js"}}})
	if resp.Error || string(resp.GetRespStaticAsset().GetContent()) != "console.log(1)" || !strings.HasPrefix(resp.GetRespStaticAsset().GetContentType(), "text/javascript") {
		t.Errorf("static asset: %+v", resp)
	}
	if page := mustProcess(t, ch, &pb.ReqEnvelope{Id: 8, Payload: &pb.ReqEnvelope_ReqGetStaticAsset{ReqGetStaticAsset: &pb.ReqGetStaticAsset{Path: "/"}}}); string(page.GetRespStaticAsset().GetContent()) != "<html>app</html>" {
		t.Errorf("the page: %+v", page)
	}
}

// The copy of the bridge identity: written by the full instance after a
// registration, 0600, only when it changes; never by the instance without
// its storage; removed when it leaves the bridge.
func TestBridgeIdentityCopy(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, cIdentityFile)
	mg := &Manager{bridgeHost: "off-the.cloud"}
	mg.identity.file = file
	mg.keepIdentity("uuid-1", "pit.off-the.cloud", "secret-1")
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the copy: %v %v", st, err)
	}
	id, err := readIdentity(file)
	if err != nil || id != (bridgeIdentity{V: 1, Bridge: "off-the.cloud", OwnerUuid: "uuid-1", Domain: "pit.off-the.cloud", Secret: "secret-1"}) {
		t.Fatalf("read %+v %v", id, err)
	}
	// The same identity again (each of the pool's registrations): not
	// written again - a rewrite would put back 0600.
	os.Chmod(file, 0o400)
	mg.keepIdentity("uuid-1", "pit.off-the.cloud", "secret-1")
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o400 {
		t.Error("rewritten without a change")
	}
	// A new process finds the same copy and doesn't write it either.
	again := &Manager{bridgeHost: "off-the.cloud"}
	again.identity.file = file
	again.keepIdentity("uuid-1", "pit.off-the.cloud", "secret-1")
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o400 {
		t.Error("rewritten by a new process")
	}
	// A rotated secret.
	os.Chmod(file, 0o600)
	mg.keepIdentity("uuid-1", "pit.off-the.cloud", "secret-2")
	if id, _ := readIdentity(file); id.Secret != "secret-2" {
		t.Errorf("after a rotation: %+v", id)
	}
	// The instance without its storage never writes one.
	off := &Manager{unavailable: true, bridgeHost: "off-the.cloud"}
	off.identity.file = filepath.Join(dir, "other.json")
	off.keepIdentity("uuid-1", "pit.off-the.cloud", "secret-3")
	if _, err := os.Stat(off.identity.file); !os.IsNotExist(err) {
		t.Error("written without the storage")
	}
	mg.dropIdentity()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("left the bridge and kept the copy")
	}
	// Never read through a link.
	os.WriteFile(filepath.Join(dir, "real.json"), []byte(`{"v":1}`), 0o600)
	os.Symlink(filepath.Join(dir, "real.json"), file)
	if _, err := readIdentity(file); err == nil {
		t.Error("read through a link")
	}
}

const cUnavailableChildEnv = "OTC_WS_UNAVAILABLE_CHILD"

// fakeBridge accepts registrations, checks the identity and hands each
// registered connection to the test.
type fakeBridge struct {
	srv   *httptest.Server
	want  *pb.BridgeRegister
	conns chan *gorilla.Conn
	mu    sync.Mutex
	dials int
}

func newFakeBridge(t *testing.T, want *pb.BridgeRegister) *fakeBridge {
	fb := &fakeBridge{want: want, conns: make(chan *gorilla.Conn, 64)}
	up := gorilla.Upgrader{}
	fb.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		fb.mu.Lock()
		fb.dials++
		fb.mu.Unlock()
		_, data, err := c.ReadMessage()
		if err != nil {
			c.Close()
			return
		}
		var env pb.ReqEnvelope
		proto.Unmarshal(data, &env)
		reg := env.GetReqBridgeRegister()
		ok := reg != nil && reg.OwnerUuid == fb.want.OwnerUuid && reg.Domain == fb.want.Domain && reg.Secret == fb.want.Secret
		ack, _ := proto.Marshal(&pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespBridgeAckOnboard{RespBridgeAckOnboard: &pb.BridgeAckOnboard{Ok: ok}}})
		c.WriteMessage(gorilla.BinaryMessage, ack)
		if !ok {
			c.Close()
			return
		}
		fb.conns <- c
	}))
	t.Cleanup(fb.srv.Close)
	prev := dialBridge
	dialBridge = func(_ string, h http.Header) (*wsframe.Client, error) {
		return wsframe.Dial("ws://"+fb.srv.Listener.Addr().String()+"/ws", h)
	}
	t.Cleanup(func() { dialBridge = prev })
	return fb
}

func (fb *fakeBridge) dialled() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.dials
}

func ask(t *testing.T, c *gorilla.Conn, env *pb.ReqEnvelope) *pb.RespEnvelope {
	t.Helper()
	b, _ := proto.Marshal(env)
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	return &resp
}

// Through the bridge: the full instance keeps a copy of the identity the
// bridge took; the instance without its storage registers with that copy,
// answers storage_unavailable over it, and once stopped closes every
// connection and dials no more.
func TestUnavailableThroughTheBridge(t *testing.T) {
	if os.Getenv(cUnavailableChildEnv) != "1" {
		// In a child process: it loads a config, which can't be unloaded.
		name := "TestUnavailableThroughTheBridge"
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), cUnavailableChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("in its own process: %v\n%s", err, out)
		}
		return
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "etc"), 0o750)
	os.WriteFile(filepath.Join(dir, "etc", "otc_wsunavail.ini"), []byte("[otc]\nbridge-addr=bridge.test\n"), 0o600)
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)
	if err := cfg.Init("otc", "wsunavail"); err != nil {
		t.Fatal(err)
	}
	identity := &pb.BridgeRegister{OwnerUuid: "uuid-1", Domain: "pit.bridge.test", Secret: "s3cret"}
	fb := newFakeBridge(t, identity)

	// The full instance: its first registration writes the copy.
	full := &Manager{settings: settings.Offline(identity.OwnerUuid, identity.Domain, identity.Secret), poolTarget: 1}
	full.identity.file = filepath.Join(dir, cIdentityFile)
	full.ensureBridgePool()
	var fullConn *gorilla.Conn
	select {
	case fullConn = <-fb.conns:
	case <-time.After(10 * time.Second):
		t.Fatal("the full instance never registered")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if id, err := readIdentity(full.identity.file); err == nil {
			raw, _ := json.Marshal(id)
			if id.Bridge != "bridge.test" || id.Domain != identity.Domain || id.Secret != identity.Secret || id.OwnerUuid != identity.OwnerUuid {
				t.Fatalf("the copy: %s", raw)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no copy of the identity")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The full instance stops before the other starts, as systemd orders it.
	full.closed.Store(true)
	fullConn.Close()
	time.Sleep(100 * time.Millisecond)
	select {
	case c := <-fb.conns:
		c.Close()
		t.Fatal("the full instance dialled again once stopped")
	default:
	}

	// Without the storage: the same identity, a pool of its own.
	static := t.TempDir()
	os.WriteFile(filepath.Join(static, "index.html"), []byte("<html>app</html>"), 0o644)
	u := NewUnavailable(static+"/", false)
	var conns []*gorilla.Conn
	for len(conns) < cUnavailablePool {
		select {
		case c := <-fb.conns:
			conns = append(conns, c)
		case <-time.After(10 * time.Second):
			t.Fatalf("%d of %d registered", len(conns), cUnavailablePool)
		}
	}
	// As the bridge relays a client: who it is, then the app's requests.
	c := conns[0]
	if r := ask(t, c, &pb.ReqEnvelope{Id: 0, Payload: &pb.ReqEnvelope_ReqBridgeClientInfo{ReqBridgeClientInfo: &pb.BridgeClientInfo{RemoteAddr: "198.51.100.7"}}}); !r.Error {
		t.Errorf("client info: %+v", r)
	}
	r := ask(t, c, &pb.ReqEnvelope{Id: 1, Lang: "de", Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}})
	if !r.Error || r.ErrorCode != CodeStorageUnavailable || r.ErrorMessage != i18n.DevErrStorageUnavailable().Render("de") {
		t.Errorf("GetPubKey: %+v", r)
	}
	if page := ask(t, conns[1], &pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqGetStaticAsset{ReqGetStaticAsset: &pb.ReqGetStaticAsset{Path: "/index.html"}}}); string(page.GetRespStaticAsset().GetContent()) != "<html>app</html>" {
		t.Errorf("the page through the bridge: %+v", page)
	}

	// Stopped: every connection closed - taken or ready - and no more dials.
	u.Stop()
	for i, c := range conns {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := c.ReadMessage(); err == nil {
			t.Errorf("connection %d still open", i)
		}
	}
	before := fb.dialled()
	time.Sleep(300 * time.Millisecond)
	if after := fb.dialled(); after != before {
		t.Errorf("dialled %d more after Stop", after-before)
	}
}
