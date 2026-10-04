// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"sync"
	"testing"
	"time"
)

// A one-off request waits for the device to open a connection rather than
// failing at once (a busy page load spends several), and gives up after
// the wait.
func TestTakeAvailableWaitsForAConnection(t *testing.T) {
	pool := &bridgePool{lock: &sync.Mutex{}}
	if c := takeAvailable(pool, 100*time.Millisecond); c != nil {
		t.Fatal("got a connection from an empty pool")
	}
	want := &deviceRelay{}
	go func() {
		time.Sleep(150 * time.Millisecond)
		pool.lock.Lock()
		pool.availableConns = append(pool.availableConns, want)
		pool.lock.Unlock()
	}()
	if c := takeAvailable(pool, 2*time.Second); c != want {
		t.Fatal("did not get the connection the device opened")
	}
}
