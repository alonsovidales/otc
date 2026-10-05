// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	gorilla "github.com/gorilla/websocket"
)

// pooledRelay registers a relay for owner in domain's pool the way
// ReqBridgeRegister does - idle, or as if a client had paired with it -
// and returns a channel closed when it dies.
func pooledRelay(t *testing.T, mg *Manager, domain, owner string, idle bool) (*deviceRelay, <-chan struct{}) {
	t.Helper()
	srv, wsURL := newEchoDeviceServer(t, func(int32) time.Duration { return 0 })
	t.Cleanup(srv.Close)
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	died := make(chan struct{})
	var relay *deviceRelay
	relay = newIdleDeviceRelay(conn, func() { mg.onDeviceConnectionDied(domain, relay); close(died) })
	relay.owner = owner
	t.Cleanup(func() { relay.Close() })

	mg.bridgesMu.Lock()
	pool, ok := mg.bridges[domain]
	if !ok {
		pool = &bridgePool{lock: new(sync.Mutex)}
		mg.bridges[domain] = pool
	}
	mg.bridgesMu.Unlock()
	pool.lock.Lock()
	relay.registeredAt = time.Now()
	if idle {
		pool.availableConns = append(pool.availableConns, relay)
	}
	if pool.relays == nil {
		pool.relays = map[*deviceRelay]struct{}{}
	}
	pool.relays[relay] = struct{}{}
	mg.onDeviceConnectionRegistered(domain, pool)
	relay.start()
	pool.lock.Unlock()
	return relay, died
}

func stopOfflineTimer(mg *Manager, domain string) {
	mg.bridgesMu.RLock()
	pool := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	if pool == nil {
		return
	}
	pool.lock.Lock()
	if pool.offlineTimer != nil {
		pool.offlineTimer.Stop()
	}
	pool.lock.Unlock()
}

func waitDead(t *testing.T, died <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-died:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s is still open", what)
	}
}

// Releasing a domain closes the connections clients are using too, not
// just the idle ones: those went on relaying for a name no longer its.
func TestDropPoolClosesPairedRelaysToo(t *testing.T) {
	mg := &Manager{bridges: map[string]*bridgePool{}}
	defer stopOfflineTimer(mg, "pit.otc")
	_, idleDied := pooledRelay(t, mg, "pit.otc", "owner", true)
	_, pairedDied := pooledRelay(t, mg, "pit.otc", "owner", false)

	mg.DropDomains([]string{"pit.otc"})
	waitDead(t, idleDied, "the idle relay")
	waitDead(t, pairedDied, "the paired relay")
	if n := mg.liveCount("pit.otc"); n != 0 {
		t.Errorf("liveCount = %d, want 0", n)
	}
	var none *Manager
	none.DropDomains([]string{"pit.otc"}) // a bridge without a manager (tests): no panic
}

// A device registering with the domain's current identity closes the
// relays a replaced identity still holds - idle or paired - so clients
// stop reaching the old device and its connections don't count against
// the new one's cap.
func TestRegistrationEvictsAReplacedIdentitysRelays(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	expectValidDevice(mock, "new-owner", "new-secret")

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, host := newTestBridge(t, mg)
	defer stopOfflineTimer(mg, host)
	_, oldIdle := pooledRelay(t, mg, host, "old-owner", true)
	_, oldPaired := pooledRelay(t, mg, host, "old-owner", false)

	c := dial()
	if err := c.WriteMessage(gorilla.BinaryMessage, registerFrame(t, host, "new-owner", "new-secret")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readResp(t, c).Payload.(*pb.RespEnvelope_RespBridgeAckOnboard); !ok {
		t.Fatal("the new identity's registration was not acknowledged")
	}
	waitDead(t, oldIdle, "the replaced identity's idle relay")
	waitDead(t, oldPaired, "the replaced identity's paired relay")
	pool := waitForPool(t, mg, host, func(p *bridgePool) bool { return len(p.availableConns) == 1 && p.liveCount == 1 })
	pool.lock.Lock()
	owner := pool.availableConns[0].owner
	pool.lock.Unlock()
	if owner != "new-owner" {
		t.Errorf("the pool kept %q's relay", owner)
	}
	c.Close()
	waitForPool(t, mg, host, func(p *bridgePool) bool { return p.liveCount == 0 })
}

// The sweep closes what a lost cluster drop left behind: the relays of a
// domain gone from the database, and a replaced identity's; the current
// identity's stay.
func TestSweepClosesUnregisteredAndReplacedRelays(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `domain`, `owner_uuid` from `devices` where `domain` in \\(\\?, \\?\\)").
		WillReturnRows(sqlmock.NewRows([]string{"domain", "owner_uuid"}).AddRow("kept.otc", "new-owner"))
	mock.ExpectQuery("select `owner_uuid` from `devices` where `domain` = \\?").
		WithArgs("gone.otc").
		WillReturnError(sql.ErrNoRows)

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	defer stopOfflineTimer(mg, "gone.otc")
	_, goneDied := pooledRelay(t, mg, "gone.otc", "owner", true)
	_, oldDied := pooledRelay(t, mg, "kept.otc", "old-owner", false)
	_, currentDied := pooledRelay(t, mg, "kept.otc", "new-owner", true)

	mg.sweepStale()
	waitDead(t, goneDied, "the unregistered domain's relay")
	waitDead(t, oldDied, "the replaced identity's relay")
	select {
	case <-currentDied:
		t.Fatal("the current identity's relay was closed")
	case <-time.After(100 * time.Millisecond):
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The sweep's answer from the database is as old as its query: a device
// that registered since - given a new identity, or a name claimed again,
// while the sweep ran - was checked after it, and its relays stay.
func TestSweepLeavesRelaysRegisteredAfterItsQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `domain`, `owner_uuid` from `devices` where `domain` in \\(\\?, \\?\\)").
		WillReturnRows(sqlmock.NewRows([]string{"domain", "owner_uuid"}).AddRow("kept.otc", "old-owner"))
	mock.ExpectQuery("select `owner_uuid` from `devices` where `domain` = \\?").
		WithArgs("claimed.otc").
		WillReturnError(sql.ErrNoRows)

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	defer stopOfflineTimer(mg, "kept.otc")
	defer stopOfflineTimer(mg, "claimed.otc")
	_, staleDied := pooledRelay(t, mg, "kept.otc", "older-owner", true)
	newRelay, newDied := pooledRelay(t, mg, "kept.otc", "new-owner", true)
	claimed, claimedDied := pooledRelay(t, mg, "claimed.otc", "owner", true)
	registeredLater(mg, "kept.otc", newRelay)
	registeredLater(mg, "claimed.otc", claimed)

	mg.sweepStale()
	waitDead(t, staleDied, "the relay of an identity replaced before the query")
	select {
	case <-newDied:
		t.Fatal("the sweep closed a relay of the identity registered after its query")
	case <-claimedDied:
		t.Fatal("the sweep closed a relay of the name registered after its query")
	case <-time.After(100 * time.Millisecond):
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// registeredLater makes relay one that went into its pool after any sweep
// this test starts.
func registeredLater(mg *Manager, domain string, relay *deviceRelay) {
	mg.bridgesMu.RLock()
	pool := mg.bridges[domain]
	mg.bridgesMu.RUnlock()
	pool.lock.Lock()
	relay.registeredAt = time.Now().Add(time.Hour)
	pool.lock.Unlock()
}

// A database failure must never read as "nothing is registered": that
// would disconnect every device on the node.
func TestSweepClosesNothingOnADatabaseError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("select `domain`, `owner_uuid` from `devices`").WillReturnError(errors.New("connection refused"))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	_, died := pooledRelay(t, mg, "pit.otc", "owner", true)
	mg.sweepStale()
	select {
	case <-died:
		t.Fatal("a database error closed a relay")
	case <-time.After(100 * time.Millisecond):
	}
	if n := mg.liveCount("pit.otc"); n != 1 {
		t.Errorf("liveCount = %d, want 1", n)
	}
}
