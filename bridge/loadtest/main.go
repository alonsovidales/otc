// SPDX-License-Identifier: AGPL-3.0-or-later

// Command loadtest measures what one bridge node handles (issue #144): how
// many idle connections it holds, and how many relayed requests and bytes
// per second it moves between clients and devices.
//
// Run it against a test bridge with open registration and its own database
// - never the production one: its fake devices register domains
// (load<N>.<tld>) and every relayed message records metrics.
//
//	loadtest -addr 149.202.83.7:9443 -tld off-the.cloud idle -n 20000
//	loadtest -addr 149.202.83.7:9443 -tld off-the.cloud relay -devices 50 -clients 500 -size 256 -for 30s
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/alonsovidales/otc/proto/generated"
)

var (
	addr = flag.String("addr", "", "the bridge to test, ip:port")
	tld  = flag.String("tld", "off-the.cloud", "device domains are load<N>.<tld>")
	run  = flag.String("run", fmt.Sprint(time.Now().Unix()%100000), "a tag in the device names, so runs don't collide")
)

func dial(host string) (*gorilla.Conn, error) {
	d := gorilla.Dialer{
		HandshakeTimeout: 20 * time.Second,
		TLSClientConfig:  &tls.Config{ServerName: host},
		NetDialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, n, *addr)
		},
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
	}
	// The Host header without a port: the bridge routes by r.Host.
	c, _, err := d.Dial("wss://"+host+"/ws", map[string][]string{"Host": {host}})
	return c, err
}

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: loadtest -addr ip:port [-tld t] idle -n N | relay -devices D -clients C -size S -for T")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *addr == "" || flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	switch flag.Arg(0) {
	case "idle":
		fs := flag.NewFlagSet("idle", flag.ExitOnError)
		n := fs.Int("n", 1000, "connections to open and hold")
		hold := fs.Duration("hold", 20*time.Second, "how long to hold them once open")
		fs.Parse(flag.Args()[1:])
		idle(*n, *hold)
	case "relay":
		fs := flag.NewFlagSet("relay", flag.ExitOnError)
		devices := fs.Int("devices", 20, "fake devices")
		clients := fs.Int("clients", 100, "concurrent clients, spread over the devices")
		size := fs.Int("size", 256, "bytes in each device answer")
		upload := fs.Int("upload", 0, "bytes in each client request")
		dur := fs.Duration("for", 30*time.Second, "how long to run")
		fs.Parse(flag.Args()[1:])
		relay(*devices, *clients, *size, *upload, *dur)
	default:
		flag.Usage()
		os.Exit(2)
	}
}

// idle opens n client connections to the bridge's own host and holds
// them: what a node pays per connection that is just there.
func idle(n int, hold time.Duration) {
	var ok, failed atomic.Int64
	conns := make([]*gorilla.Conn, n)
	sem := make(chan struct{}, 200)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			c, err := dial(*tld)
			if err != nil {
				if failed.Add(1) <= 5 {
					fmt.Println("dial:", err)
				}
				return
			}
			conns[i] = c
			ok.Add(1)
		}(i)
	}
	wg.Wait()
	fmt.Printf("idle: %d open, %d failed, in %s (%.0f/s)\n", ok.Load(), failed.Load(), time.Since(start).Round(time.Millisecond), float64(ok.Load())/time.Since(start).Seconds())
	time.Sleep(hold)
	alive := 0
	for _, c := range conns {
		if c == nil {
			continue
		}
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if c.WriteMessage(gorilla.PingMessage, nil) == nil {
			alive++
		}
		c.Close()
	}
	fmt.Printf("idle: %d still alive after %s\n", alive, hold)
}

// device registers one pool connection for domain and answers every
// request with an answer of size bytes, the request's id echoed.
func device(domain, owner, secret string, size int, ready *sync.WaitGroup) {
	c, err := dial(*tld)
	if err != nil {
		fmt.Println("device dial:", err)
		ready.Done()
		return
	}
	b, _ := proto.Marshal(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqBridgeRegister{ReqBridgeRegister: &pb.BridgeRegister{OwnerUuid: owner, Domain: domain, Secret: secret}}})
	c.WriteMessage(gorilla.BinaryMessage, b)
	_, data, err := c.ReadMessage()
	var r pb.RespEnvelope
	if err != nil || proto.Unmarshal(data, &r) != nil || r.Error {
		fmt.Println("device register:", err, r.ErrorMessage)
		ready.Done()
		return
	}
	ready.Done()
	payload := make([]byte, size)
	var wmu sync.Mutex
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		go func(data []byte) {
			var req pb.ReqEnvelope
			if proto.Unmarshal(data, &req) != nil {
				return
			}
			resp := &pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}
			if req.Id != 0 && size > 0 {
				resp.Payload = &pb.RespEnvelope_RespFile{RespFile: &pb.File{Content: payload}}
			}
			out, _ := proto.Marshal(resp)
			wmu.Lock()
			c.WriteMessage(gorilla.BinaryMessage, out)
			wmu.Unlock()
		}(data)
	}
}

// relay runs devices fake devices with enough pool connections for
// clients clients, then has every client send requests back to back for
// dur: requests per second, latency and bytes moved through one node.
func relay(devices, clients, size, upload int, dur time.Duration) {
	perDevice := (clients + devices - 1) / devices
	var ready sync.WaitGroup
	domains := make([]string, devices)
	for i := range domains {
		domains[i] = fmt.Sprintf("load%s-%d.%s", *run, i, *tld)
		owner, secret := uuid.NewString(), uuid.NewString()
		for j := 0; j < perDevice; j++ {
			ready.Add(1)
			go device(domains[i], owner, secret, size, &ready)
			if j == 0 {
				// The first registration creates the domain (open
				// registration); the rest join its pool.
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
	ready.Wait()
	fmt.Printf("relay: %d devices x %d pool connections registered\n", devices, perDevice)

	var reqs, errs, bytes atomic.Int64
	lat := make([][]time.Duration, clients)
	stop := time.Now().Add(dur)
	body := make([]byte, upload)
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := dial(domains[i%devices])
			if err != nil {
				errs.Add(1)
				return
			}
			defer c.Close()
			id := int32(1)
			for time.Now().Before(stop) {
				id++
				env := &pb.ReqEnvelope{Id: id, Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}}
				if upload > 0 {
					env.Payload = &pb.ReqEnvelope_ReqUploadChunk{ReqUploadChunk: &pb.UploadChunk{UploadId: "x", Data: body}}
				}
				b, _ := proto.Marshal(env)
				t0 := time.Now()
				if c.WriteMessage(gorilla.BinaryMessage, b) != nil {
					errs.Add(1)
					return
				}
				_, data, err := c.ReadMessage()
				if err != nil {
					errs.Add(1)
					return
				}
				lat[i] = append(lat[i], time.Since(t0))
				reqs.Add(1)
				bytes.Add(int64(len(b) + len(data)))
			}
		}(i)
	}
	wg.Wait()
	var all []time.Duration
	for _, l := range lat {
		all = append(all, l...)
	}
	sort.Slice(all, func(a, b int) bool { return all[a] < all[b] })
	pct := func(p float64) time.Duration {
		if len(all) == 0 {
			return 0
		}
		return all[min(len(all)-1, int(float64(len(all))*p))]
	}
	secs := dur.Seconds()
	fmt.Printf("relay: %d clients, answer %d B, request %d B: %.0f req/s, %.1f MB/s, p50 %s p99 %s max %s, %d errors\n",
		clients, size, upload, float64(reqs.Load())/secs, float64(bytes.Load())/secs/1e6,
		pct(0.5).Round(10*time.Microsecond), pct(0.99).Round(10*time.Microsecond), pct(1).Round(time.Millisecond), errs.Load())
}
