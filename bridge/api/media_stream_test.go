// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/websocket"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// latentDevice is a device behind a home line, on the far side of a real
// bridge: it registers conns connections, answers each media range after
// rtt, plus cold for the first request on a connection (one that sat in
// the pool idle restarts its TCP window: slow start after idle), and
// handles at most 4 requests of a connection at once, as a device does for
// one that hasn't signed in. It counts the requests each connection got.
type latentDevice struct {
	file      []byte
	rtt, cold time.Duration

	mu      sync.Mutex
	perConn []int
}

func startLatentDevice(t *testing.T, bridgeURL, domain string, conns int, d *latentDevice) {
	t.Helper()
	for i := 0; i < conns; i++ {
		c, _, err := gorilla.DefaultDialer.Dial(bridgeURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		reg, _ := proto.Marshal(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqBridgeRegister{
			ReqBridgeRegister: &pb.BridgeRegister{OwnerUuid: "owner", Domain: domain, Secret: "secret"},
		}})
		if err := c.WriteMessage(gorilla.BinaryMessage, reg); err != nil {
			t.Fatal(err)
		}
		_, ack, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var resp pb.RespEnvelope
		if proto.Unmarshal(ack, &resp) != nil || resp.GetRespBridgeAckOnboard() == nil {
			t.Fatalf("registration refused: %v", &resp)
		}
		d.mu.Lock()
		n := len(d.perConn)
		d.perConn = append(d.perConn, 0)
		d.mu.Unlock()
		go d.serve(c, n)
	}
}

func (d *latentDevice) serve(c *gorilla.Conn, n int) {
	var writeMu sync.Mutex
	inFlight := make(chan struct{}, 4)
	for first := true; ; first = false {
		inFlight <- struct{}{}
		_, frame, err := c.ReadMessage()
		if err != nil {
			return
		}
		var req pb.ReqEnvelope
		if proto.Unmarshal(frame, &req) != nil {
			return
		}
		d.mu.Lock()
		d.perConn[n]++
		d.mu.Unlock()
		wait := d.rtt
		if first {
			wait += d.cold
		}
		go func(req *pb.ReqEnvelope) {
			defer func() { <-inFlight }()
			time.Sleep(wait)
			rq := req.GetReqGetMediaRange()
			size := int64(len(d.file))
			end := min(rq.Offset+min(rq.Length, maxProxiedRange), size)
			b, _ := proto.Marshal(&pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespMediaRange{RespMediaRange: &pb.RespMediaRange{
				Content: d.file[rq.Offset:end], Offset: rq.Offset, TotalSize: size, Mime: "video/mp4",
			}}})
			writeMu.Lock()
			defer writeMu.Unlock()
			c.WriteMessage(gorilla.BinaryMessage, b)
		}(&req)
	}
}

func (d *latentDevice) requests() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int(nil), d.perConn...)
}

// Through a real bridge, a media response takes one of the device's
// connections for all its spans and keeps the next ones on their way: on
// a device with a round trip and a cold start per connection, it plays
// in a fraction of the time the one-off way took, a fresh connection and a
// round trip per span, one after another.
func TestAMediaResponseStreamsOverOneConnection(t *testing.T) {
	const (
		domain = "pit.off-the.cloud"
		spans  = 8
		conns  = spans + 4
	)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < conns; i++ {
		mock.ExpectQuery("select `owner_uuid`, `secret` from `devices` where `domain` = \\?").WithArgs(domain).
			WillReturnRows(sqlmock.NewRows([]string{"owner_uuid", "secret"}).AddRow("owner", "secret"))
	}
	mg := websocket.Init("", dao.NewWithDB(db))
	bridge := httptest.NewServer(http.HandlerFunc(mg.Listen))
	t.Cleanup(bridge.Close)
	dev := &latentDevice{file: mediaFile(spans), rtt: 40 * time.Millisecond, cold: 80 * time.Millisecond}
	startLatentDevice(t, "ws"+strings.TrimPrefix(bridge.URL, "http")+"/ws", domain, conns, dev)
	deadline := time.Now().Add(5 * time.Second)
	for mg.FleetStats().Idle < conns {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d connections registered", mg.FleetStats().Idle, conns)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The way it was: a one-off per span, each on a fresh connection, the
	// next asked for once the last was in.
	start := time.Now()
	for i := int64(0); i < spans; i++ {
		frame, _ := proto.Marshal(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqGetMediaRange{
			ReqGetMediaRange: &pb.ReqGetMediaRange{Token: "tok", Offset: i * maxProxiedRange, Length: maxProxiedRange},
		}})
		if _, err := mg.ForwardOneOff(domain, frame); err != nil {
			t.Fatalf("one-off span %d: %v", i, err)
		}
	}
	oneOff := time.Since(start)
	before := dev.requests()

	api := &API{websocket: mg}
	req := httptest.NewRequest(http.MethodGet, "/media/tok", nil)
	req.Host = domain
	req.SetPathValue("token", "tok")
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()
	start = time.Now()
	api.proxyMedia(rec, req)
	streamed := time.Since(start)

	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), dev.file) {
		t.Fatalf("status %d, %d bytes: not the whole file", rec.Code, rec.Body.Len())
	}
	after := dev.requests()
	used := 0
	for i := range after {
		if after[i] > before[i] {
			used++
		}
	}
	if used != 1 {
		t.Errorf("the response's spans went over %d connections, want 1", used)
	}
	t.Logf("%d spans of 4 MiB: %v the one-off way; the whole file, %d spans and a bit, %v streamed", spans, oneOff, spans, streamed)
	// About three times as fast here, twice under the race detector; the
	// bound leaves room for a loaded machine.
	if streamed > oneOff*3/4 {
		t.Errorf("streamed in %v, hardly faster than the one-off way (%v)", streamed, oneOff)
	}
}
