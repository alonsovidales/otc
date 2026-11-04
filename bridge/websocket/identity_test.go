// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	stdlog "log"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func TestPlausibleDomain(t *testing.T) {
	for d, want := range map[string]bool{
		"casa.off-the.cloud":     true,
		"Casa.Off-The.Cloud":     true,
		"my_pi.off-the.cloud":    true,
		"127.0.0.1:5555":         true,
		"[::1]:8443":             true,
		"":                       false,
		"casa .off-the.cloud":    false,
		"casa.off-the.cloud\n":   false,
		"cása.off-the.cloud":     false,
		strings.Repeat("a", 150): true,
		strings.Repeat("a", 151): false,
	} {
		if got := plausibleDomain(d); got != want {
			t.Errorf("plausibleDomain(%q) = %v, want %v", d, got, want)
		}
	}
	if logged(strings.Repeat("x", 1<<20)) != strings.Repeat("x", cLoggedMax)+"...(1048576 bytes)" {
		t.Error("a long value is not cut for the log")
	}
	if logged("casa") != "casa" {
		t.Error("a short value was changed for the log")
	}
}

// captureLog collects what the log package writes until the test ends.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	stdlog.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	t.Cleanup(func() { stdlog.SetOutput(stdlogDefault) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

var stdlogDefault = stdlog.Writer()

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// A device's message whose identity no device can have - a domain or an
// owner past their columns, a domain no client could reach - is refused
// as a wrong secret before anything in it is looked up (a lookup here
// would fail, and answer "internal error") or logged.
func TestImplausibleIdentitiesAreRefusedBeforeTheyAreUsed(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, _ := newTestBridge(t, mg)
	logs := captureLog(t)

	const marker = "MARKER"
	longDomain := marker + strings.Repeat("d", 1<<20)
	envs := func(owner, domain, secret string) []*pb.ReqEnvelope {
		return []*pb.ReqEnvelope{
			{Payload: &pb.ReqEnvelope_ReqBridgeRegister{ReqBridgeRegister: &pb.BridgeRegister{OwnerUuid: owner, Domain: domain, Secret: secret}}},
			{Payload: &pb.ReqEnvelope_ReqRotateBridgeSecret{ReqRotateBridgeSecret: &pb.RotateBridgeSecret{OwnerUuid: owner, Domain: domain, Secret: secret}}},
			{Payload: &pb.ReqEnvelope_ReqBridgeReleaseDomain{ReqBridgeReleaseDomain: &pb.BridgeReleaseDomain{OwnerUuid: owner, Domain: domain, Secret: secret}}},
			{Payload: &pb.ReqEnvelope_ReqBridgeSendLogs{ReqBridgeSendLogs: &pb.BridgeSendLogs{OwnerUuid: owner, Domain: domain, Secret: secret}}},
			{Payload: &pb.ReqEnvelope_ReqIsDomainAvailable{ReqIsDomainAvailable: &pb.ReqIsDomainAvailable{OwnerUuid: owner, Domain: domain, Secret: secret}}},
			{Payload: &pb.ReqEnvelope_ReqRegisterUserDomain{ReqRegisterUserDomain: &pb.ReqRegisterUserDomain{OwnerUuid: owner, Domain: domain, Secret: secret}}},
			{Payload: &pb.ReqEnvelope_ReqSetDeviceDisabled{ReqSetDeviceDisabled: &pb.ReqSetDeviceDisabled{OwnerUuid: owner, Domain: domain, Secret: secret}}},
			{Payload: &pb.ReqEnvelope_ReqBridgeNotify{ReqBridgeNotify: &pb.BridgeNotify{OwnerUuid: owner, Domain: domain, Secret: secret}}},
			{Payload: &pb.ReqEnvelope_ReqUpdatePushRegistrations{ReqUpdatePushRegistrations: &pb.UpdatePushRegistrations{OwnerUuid: owner, Domain: domain, Secret: secret}}},
		}
	}
	var all []*pb.ReqEnvelope
	all = append(all, envs("owner", longDomain, "secret")...)
	all = append(all, envs(marker+strings.Repeat("o", 64), "casa.off-the.cloud", "secret")...)
	all = append(all, envs("owner", "casa.off-the.cloud", marker+strings.Repeat("s", 150))...)
	all = append(all, envs("owner", marker+" casa.off-the.cloud", "secret")...)
	for i, env := range all {
		env.Id = int32(i + 1)
		b, err := proto.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		c := dial()
		if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
			t.Fatal(err)
		}
		resp := readResp(t, c)
		if !resp.Error || resp.ErrorMessage != relayText(i18n.ApiRelayInvalidSecret()) {
			t.Errorf("%T: answered %v %q, want the wrong-secret refusal", env.Payload, resp.Error, resp.ErrorMessage)
		}
	}
	if strings.Contains(logs(), marker) {
		t.Error("a refused identity reached the log")
	}
}
