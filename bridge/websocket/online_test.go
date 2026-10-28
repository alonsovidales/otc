// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	gorilla "github.com/gorilla/websocket"

	pb "github.com/alonsovidales/otc/proto/generated"

	"github.com/alonsovidales/otc/bridge/dao"
)

// A device's first connection to this node is queued for its online
// stamp, and so is its last one closing - not every connection in between.
func TestDeviceConnectionsQueueOnlineStamps(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	expectValidDevice(mock, "owner-uuid", "secret")
	expectValidDevice(mock, "owner-uuid", "secret")

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}, onlineEvents: make(chan onlineEvent, 16)}
	dial, _ := newTestBridge(t, mg)
	var conns []*gorilla.Conn
	for i := 0; i < 2; i++ {
		c := dial()
		if err := c.WriteMessage(gorilla.BinaryMessage, registerFrame(t, "pit.otc", "owner-uuid", "secret")); err != nil {
			t.Fatal(err)
		}
		if _, ok := readResp(t, c).Payload.(*pb.RespEnvelope_RespBridgeAckOnboard); !ok {
			t.Fatal("no registration ack")
		}
		conns = append(conns, c)
		n := i + 1
		waitForPool(t, mg, "pit.otc", func(p *bridgePool) bool { return p.liveCount == n })
	}
	next := func() onlineEvent {
		t.Helper()
		select {
		case ev := <-mg.onlineEvents:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no online event")
		}
		return onlineEvent{}
	}
	if ev := next(); ev != (onlineEvent{"pit.otc", false}) {
		t.Fatalf("first connection: %+v", ev)
	}

	conns[0].Close()
	waitForPool(t, mg, "pit.otc", func(p *bridgePool) bool { return p.liveCount == 1 })
	select {
	case ev := <-mg.onlineEvents:
		t.Fatalf("an event while a connection is left: %+v", ev)
	default:
	}
	conns[1].Close()
	pool := waitForPool(t, mg, "pit.otc", func(p *bridgePool) bool { return p.liveCount == 0 })
	if ev := next(); ev != (onlineEvent{"pit.otc", true}) {
		t.Fatalf("last connection closed: %+v", ev)
	}
	pool.lock.Lock()
	if pool.offlineTimer != nil {
		pool.offlineTimer.Stop()
	}
	pool.lock.Unlock()
}

// onlineLoop stamps the devices connected to this node every tick (the dao
// throttles each), and the queued arrivals and departures as they come.
func TestOnlineLoopStampsTheConnectedDevices(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	const stamp = "update `devices` set `last_online_at` = \\? where `domain` in \\(\\?\\)"
	mock.ExpectExec(stamp).WithArgs(sqlmock.AnyArg(), "held.otc", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(stamp).WithArgs(sqlmock.AnyArg(), "gone.otc", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(stamp).WithArgs(sqlmock.AnyArg(), "new.otc", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))

	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{
		"held.otc": {lock: new(sync.Mutex), liveCount: 2},
		"idle.otc": {lock: new(sync.Mutex)}, // no live connection: not stamped
	}, onlineEvents: make(chan onlineEvent, 16)}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { mg.onlineLoop(10*time.Millisecond, stop); close(done) }()
	mg.queueOnline("gone.otc", true)
	mg.queueOnline("new.otc", false)

	deadline := time.Now().Add(5 * time.Second)
	for mock.ExpectationsWereMet() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-done
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Without a database (a manager built for tests, or none), nothing is
// queued and nothing blocks.
func TestQueueOnlineWithoutALoop(t *testing.T) {
	mg := &Manager{bridges: map[string]*bridgePool{}}
	mg.queueOnline("pit.otc", false)
	full := &Manager{onlineEvents: make(chan onlineEvent)}
	full.queueOnline("pit.otc", true) // nobody reading: dropped, not blocked
}
