// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// A connection that goes while it waits for its key stops waiting and
// takes none: they used to be made for connections long gone, each behind
// the one before.
func TestAKeyIsNotTakenForAConnectionThatWent(t *testing.T) {
	pool := make(chan *rsa.PrivateKey, 1)
	gone := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := takeKey(pool, &addrTurns{per: 1}, "203.0.113.9", gone)
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	close(gone)
	select {
	case err := <-done:
		if !errors.Is(err, errPeerGone) {
			t.Fatalf("got %v, want errPeerGone", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the connection went")
	}
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pool <- key
	if len(pool) != 1 {
		t.Fatal("a key was taken for a connection that went")
	}
}

// Keys come in turn by address: however many connections one address has
// waiting, another address's sign-in waits behind cKeyWaitsPerAddr of
// them, not all.
func TestKeysComeInTurnByAddress(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pool := make(chan *rsa.PrivateKey)
	turns := &addrTurns{per: cKeyWaitsPerAddr}
	never := make(chan struct{})
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	wait := func(who, addr string) {
		defer wg.Done()
		if _, err := takeKey(pool, turns, addr, never); err != nil {
			t.Error(err)
		}
		mu.Lock()
		order = append(order, who)
		mu.Unlock()
	}
	for i := range 20 {
		wg.Add(1)
		go wait(fmt.Sprint("flood", i), "203.0.113.9")
	}
	time.Sleep(50 * time.Millisecond)
	wg.Add(1)
	go wait("owner", "192.168.1.20")
	time.Sleep(50 * time.Millisecond)
	for range 21 {
		pool <- key
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	for i, who := range order {
		if who == "owner" {
			if i > cKeyWaitsPerAddr {
				t.Errorf("the owner's key came %dth, behind %v", i+1, order[:i])
			}
			return
		}
	}
	t.Fatal("the owner got no key")
}
