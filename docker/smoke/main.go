// SPDX-License-Identifier: AGPL-3.0-or-later

// otc-smoke signs in to a freshly started container the way the apps do,
// for the image's tests before it is published (.github/workflows/docker.yml):
//
//	otc-smoke -addr 127.0.0.1:8080 -password-file pw.txt -release 141
//
// The owner password must already be set (otc-entrypoint set-password). It
// checks that the device says it runs in a container and has its password,
// that a wrong password is refused, that the right one signs in, and that the
// signed-in device reports the expected release. Exit status 1 on the first
// failure, with what went wrong.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

const answerWithin = 60 * time.Second

type conn struct {
	ws     *websocket.Conn
	nextID int32
}

func dial(addr string) (*conn, error) {
	u := url.URL{Scheme: "ws", Host: addr, Path: "/ws"}
	ws, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, err
	}
	return &conn{ws: ws}, nil
}

// request sends one request and waits for the answer with its id; anything
// else the device sends meanwhile (pushed events) is skipped.
func (c *conn) request(req *pb.ReqEnvelope) (*pb.RespEnvelope, error) {
	c.nextID++
	req.Id = c.nextID
	b, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(answerWithin)
	for {
		_ = c.ws.SetReadDeadline(deadline)
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return nil, err
		}
		var resp pb.RespEnvelope
		if err := proto.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("unreadable answer: %w", err)
		}
		if resp.Id == req.Id {
			if resp.Error {
				return nil, fmt.Errorf("device error: %s", resp.ErrorMessage)
			}
			return &resp, nil
		}
	}
}

func (c *conn) pubKey() (*pb.PubKey, error) {
	resp, err := c.request(&pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}})
	if err != nil {
		return nil, err
	}
	p, ok := resp.Payload.(*pb.RespEnvelope_RespPubKey)
	if !ok {
		return nil, fmt.Errorf("GetPubKey answered %T", resp.Payload)
	}
	return p.RespPubKey, nil
}

// signIn is GetPubKey, then Auth with the password sealed to that key.
func (c *conn) signIn(password string) (*pb.Ack, *pb.PubKey, error) {
	pk, err := c.pubKey()
	if err != nil {
		return nil, nil, err
	}
	pubAny, err := x509.ParsePKIXPublicKey(pk.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("public key: %w", err)
	}
	rsaKey, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("public key is %T, not RSA", pubAny)
	}
	sealed, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaKey, []byte(password), nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.request(&pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Key: sealed}}})
	if err != nil {
		return nil, nil, err
	}
	a, ok := resp.Payload.(*pb.RespEnvelope_RespAck)
	if !ok {
		return nil, nil, fmt.Errorf("Auth answered %T", resp.Payload)
	}
	return a.RespAck, pk, nil
}

func run(addr, password string, release int32) error {
	// A wrong password, on a connection of its own.
	c, err := dial(addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	ack, pk, err := c.signIn(password + "-wrong")
	c.ws.Close()
	if err != nil {
		return fmt.Errorf("sign-in with a wrong password: %w", err)
	}
	if pk.IsNewDevice {
		return errors.New("the device says it has no owner password yet")
	}
	if !pk.Container {
		return errors.New("the device doesn't say it runs in a container")
	}
	if ack.Ok {
		return errors.New("a wrong password signed in")
	}
	log.Printf("ok: a wrong password is refused (%s)", ack.ErrorMsg)

	c, err = dial(addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer c.ws.Close()
	if ack, _, err = c.signIn(password); err != nil {
		return fmt.Errorf("sign-in: %w", err)
	}
	if !ack.Ok {
		return fmt.Errorf("the owner password was refused: %s", ack.ErrorMsg)
	}
	log.Print("ok: the owner password signs in")

	resp, err := c.request(&pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqCheckUpdate{ReqCheckUpdate: &pb.ReqCheckUpdate{}}})
	if err != nil {
		return fmt.Errorf("signed-in request: %w", err)
	}
	u, ok := resp.Payload.(*pb.RespEnvelope_RespUpdateInfo)
	if !ok {
		return fmt.Errorf("CheckUpdate answered %T", resp.Payload)
	}
	if !u.RespUpdateInfo.Container {
		return errors.New("Settings › Device version doesn't say it runs in a container")
	}
	if release > 0 && u.RespUpdateInfo.CurrentVersion != release {
		return fmt.Errorf("the device is on release %d, not %d", u.RespUpdateInfo.CurrentVersion, release)
	}
	log.Printf("ok: signed in, release %d", u.RespUpdateInfo.CurrentVersion)
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "the device's web and app port")
	pwFile := flag.String("password-file", "", "file holding the owner password (first line)")
	release := flag.Int("release", 0, "the release the device must report (0: any)")
	flag.Parse()
	log.SetFlags(0)
	b, err := os.ReadFile(*pwFile)
	if err != nil {
		log.Fatalf("FAIL: password file: %v", err)
	}
	password, _, _ := strings.Cut(string(b), "\n")
	if err := run(*addr, password, int32(*release)); err != nil {
		log.Fatalf("FAIL: %v", err)
	}
}
