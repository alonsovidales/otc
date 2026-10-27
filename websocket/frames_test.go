// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsovidales/otc/mediastream"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// A stranger's messages are budgeted past 64 KiB, in a budget of their own;
// a signed-in owner's and a friend's past 4 MiB, as before.
func TestFrameRulesBudgetStrangersPastSixtyFourKiB(t *testing.T) {
	anon := &connHandler{mg: &Manager{}}
	if limit, free, b := anon.frameRules(); limit != cPreAuthReadLimit || free != cAnonFrameFree || b != anonFrameBudget {
		t.Errorf("a stranger: limit %d, free %d, budget %p; want %d, %d, the strangers' %p", limit, free, b, cPreAuthReadLimit, cAnonFrameFree, anonFrameBudget)
	}
	friend := &connHandler{mg: &Manager{}}
	friend.setFriendProfile(&profile.Profile{})
	if limit, free, b := friend.frameRules(); limit != cPreAuthReadLimit || free != wsframe.DefaultFree || b != frameBudget {
		t.Errorf("a friend: limit %d, free %d; want %d, %d and the shared budget", limit, free, cPreAuthReadLimit, wsframe.DefaultFree)
	}
	owner := &connHandler{mg: &Manager{}}
	owner.setSession(&session.Session{})
	if limit, free, b := owner.frameRules(); limit != cOwnerReadLimit || free != wsframe.DefaultFree || b != frameBudget {
		t.Errorf("the owner: limit %d, free %d; want %d, %d and the shared budget", limit, free, cOwnerReadLimit, wsframe.DefaultFree)
	}
}

// requestCounter stands in for processMessage: it counts the requests
// being answered at once, holding each until released, and signs the
// connection in on an AuthWithToken.
type requestCounter struct {
	mu            sync.Mutex
	now, most     int
	release       chan struct{}
	signedInAfter bool
}

func (rc *requestCounter) handle(ch *connHandler, env *pb.ReqEnvelope) (*pb.RespEnvelope, bool) {
	if _, ok := env.Payload.(*pb.ReqEnvelope_ReqAuthWithToken); ok {
		ch.setSession(&session.Session{})
		return &pb.RespEnvelope{Id: env.Id, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}, false
	}
	rc.mu.Lock()
	rc.now++
	rc.most = max(rc.most, rc.now)
	release := rc.release
	rc.mu.Unlock()
	<-release
	rc.mu.Lock()
	rc.now--
	rc.mu.Unlock()
	return &pb.RespEnvelope{Id: env.Id}, false
}

func (rc *requestCounter) running() (now, most int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.now, rc.most
}

// Until it signs in, a connection has cMaxInFlightAnon requests answered at
// once and the next one waits to be read - none is refused; once signed in,
// up to cMaxInFlight.
func TestStrangersHaveAFewRequestsInFlight(t *testing.T) {
	rc := &requestCounter{release: make(chan struct{})}
	saved := handleRequest
	handleRequest = rc.handle
	served := make(chan struct{})
	// Restored once the connection's handlers are done (cleanups run last
	// registered first: after the server and the client closed).
	t.Cleanup(func() {
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("the connection was never done")
		}
		handleRequest = saved
	})

	mg := &Manager{}
	up := gorilla.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer close(served)
		defer conn.Close()
		mg.serveConnection(conn, r, nil)
	}))
	t.Cleanup(srv.Close)
	c, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	send := func(id int32, env *pb.ReqEnvelope) {
		t.Helper()
		env.Id = id
		b, _ := proto.Marshal(env)
		if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
			t.Fatal(err)
		}
	}
	replies := func(n int) {
		t.Helper()
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		for i := 0; i < n; i++ {
			if _, _, err := c.ReadMessage(); err != nil {
				t.Fatalf("reply %d of %d: %v", i+1, n, err)
			}
		}
	}
	waitRunning := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if now, _ := rc.running(); now == want {
				return
			}
			if time.Now().After(deadline) {
				now, _ := rc.running()
				t.Fatalf("%d requests answered at once, want %d", now, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	profileReq := func() *pb.ReqEnvelope {
		return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqGetProfile{ReqGetProfile: &pb.GetProfile{}}}
	}
	for i := 0; i < 10; i++ {
		send(int32(i+1), profileReq())
	}
	waitRunning(cMaxInFlightAnon)
	time.Sleep(100 * time.Millisecond) // nothing more is read meanwhile
	if _, most := rc.running(); most != cMaxInFlightAnon {
		t.Fatalf("a stranger had %d requests answered at once, want %d", most, cMaxInFlightAnon)
	}
	close(rc.release) // the rest are answered, a few at a time
	replies(10)

	// Signed in: no longer a stranger's limit.
	rc.mu.Lock()
	rc.release, rc.most = make(chan struct{}), 0
	rc.mu.Unlock()
	send(11, &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{}}})
	replies(1)
	for i := 0; i < 10; i++ {
		send(int32(20+i), profileReq())
	}
	waitRunning(10)
	rc.mu.Lock()
	close(rc.release)
	rc.mu.Unlock()
	replies(10)
}

// Issue F3 of the 2026-10 review: a span of a stream and a static file are
// read whole and packed again, and anyone can ask for them - both hold the
// content budget now, but only for a token or a file that exists.
func TestMediaRangeAndStaticAssetHoldTheBudget(t *testing.T) {
	mg := &Manager{media: mediastream.NewServer(mediastream.NewStore(), t.TempDir(), t.TempDir())}
	ch := &connHandler{mg: mg}
	token, _, err := mg.media.Store().Issue(mediastream.Resource{Kind: mediastream.KindPublicationMedia, Hash: strings.Repeat("a", 64), Mime: "video/mp4", Size: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		token  string
		length int64
		want   int64
	}{
		{token, 1000, 2000},
		{token, 0, 2 * mediastream.MaxRangeBytes},
		{token, 64 << 20, 2 * mediastream.MaxRangeBytes},
		{"not-a-token", 1000, 0},
	} {
		if got := ch.mediaRangeNeed(&pb.ReqGetMediaRange{Token: c.token, Length: c.length}); got != c.want {
			t.Errorf("token %q, %d bytes: needs %d, want %d", c.token, c.length, got, c.want)
		}
	}
	if got := (&connHandler{mg: &Manager{}}).mediaRangeNeed(&pb.ReqGetMediaRange{Token: token}); got != 0 {
		t.Errorf("no media server: %d", got)
	}

	static := t.TempDir()
	if err := os.WriteFile(filepath.Join(static, "index.html"), make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(static, "app.js"), make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	mg.staticPath = static + "/" // as [otc-api] static is
	for path, want := range map[string]int64{
		"/app.js":        10000,
		"/":              2000, // index.html
		"/a-route":       2000, // the page's own route: index.html
		"/../etc/passwd": 0,
	} {
		if got := ch.staticAssetNeed(&pb.ReqGetStaticAsset{Path: path}); got != want {
			t.Errorf("%s: needs %d, want %d", path, got, want)
		}
	}
}
