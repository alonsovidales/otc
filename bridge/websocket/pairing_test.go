// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/limits"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
)

// pairingBridge is a test bridge for one domain whose disabled check
// answers "no" n times.
func pairingBridge(t *testing.T, n int) (*Manager, func() *gorilla.Conn, string) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for i := 0; i < n; i++ {
		mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
			WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))
	}
	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, host := newTestBridge(t, mg)
	t.Cleanup(func() { stopOfflineTimer(mg, host) })
	return mg, dial, host
}

// paired reports whether a fresh client's first request was relayed
// (true) or answered "device unreachable" (false).
func paired(t *testing.T, c *gorilla.Conn) bool {
	t.Helper()
	if err := c.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, 1)); err != nil {
		t.Fatal(err)
	}
	resp := readResp(t, c)
	if ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck); ok && ack.RespAck.Code == cCodeDeviceUnreachable {
		return false
	}
	return resp.Id == 1 && !resp.Error
}

func waitNoPairings(t *testing.T, mg *Manager) {
	t.Helper()
	for i := 0; i < 300; i++ {
		mg.pairedMu.Lock()
		n := len(mg.pairedByAddr)
		mg.pairedMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a closed client's pairing was never given back")
}

// One address can hold only so many of a device's connections at once,
// and gets them back as its sockets close.
func TestOpenPairingsPerAddressAreCapped(t *testing.T) {
	setVar(t, &cMaxPairedPerAddr, 1)
	mg, dial, host := pairingBridge(t, 3)
	for i := 0; i < 3; i++ {
		pooledRelay(t, mg, host, "owner", true)
	}

	first := dial()
	if !paired(t, first) {
		t.Fatal("the first client was refused")
	}
	if paired(t, dial()) {
		t.Fatal("a second open pairing from the same address went through")
	}
	first.Close()
	waitNoPairings(t, mg)
	if !paired(t, dial()) {
		t.Fatal("the address never got its pairing back")
	}
}

// New pairings from one address are rate limited.
func TestNewPairingsPerAddressAreRateLimited(t *testing.T) {
	mg, dial, host := pairingBridge(t, 2)
	mg.pairPerAddr = limits.NewRate(0.001, 1)
	for i := 0; i < 2; i++ {
		pooledRelay(t, mg, host, "owner", true)
	}
	first := dial()
	if !paired(t, first) {
		t.Fatal("the first client was refused")
	}
	first.Close()
	waitNoPairings(t, mg)
	if paired(t, dial()) {
		t.Fatal("a pairing past the rate went through")
	}
}

// However many addresses ask, a device has at most maxPairedPerDevice of
// its connections in use.
func TestPairedConnectionsPerDeviceAreCapped(t *testing.T) {
	setVar(t, &cDefaultMaxPairedPerDevice, 1)
	mg, dial, host := pairingBridge(t, 2)
	mg.pairPerAddr = nil
	setVar(t, &cMaxPairedPerAddr, 100)
	for i := 0; i < 2; i++ {
		pooledRelay(t, mg, host, "owner", true)
	}
	if !paired(t, dial()) {
		t.Fatal("the first client was refused")
	}
	if paired(t, dial()) {
		t.Fatal("the device went past its cap of connections in use")
	}
}

func TestPairingKeyGroupsAnIPv6HostsAddresses(t *testing.T) {
	if a, b := pairingKey("2001:db8:1:2::1", "pit.otc"), pairingKey("2001:db8:1:2:ffff::9", "pit.otc"); a != b {
		t.Errorf("one /64 gave two keys: %q, %q", a, b)
	}
	if a, b := pairingKey("2001:db8:1:2::1", "pit.otc"), pairingKey("2001:db8:1:3::1", "pit.otc"); a == b {
		t.Errorf("two /64s gave one key: %q", a)
	}
	if k := pairingKey("203.0.113.7", "pit.otc"); k != "203.0.113.7|pit.otc" {
		t.Errorf("IPv4 key %q", k)
	}
	if pairingKey("203.0.113.7", "pit.otc") == pairingKey("203.0.113.7", "cala.otc") {
		t.Error("one address's pairings with two devices share a key")
	}
}
