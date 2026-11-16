// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// A device with no free connection on this node answers ErrNoConnection
// after the wait asked for, not after cOneOffConnWait: a clustered node
// then hands the request to one that has a connection.
func TestForwardOneOffWithinGivesUpAfterItsOwnWait(t *testing.T) {
	mg := &Manager{bridges: map[string]*bridgePool{"cala.off-the.cloud": {lock: new(sync.Mutex)}}}
	start := time.Now()
	_, err := mg.ForwardOneOffWithin("cala.off-the.cloud", []byte("x"), 20*time.Millisecond)
	if !errors.Is(err, ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("waited %v, want about 20 ms", took)
	}
}
