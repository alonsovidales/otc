// SPDX-License-Identifier: AGPL-3.0-or-later

package wsframe

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// resetKnown empties the remembered addresses and puts the resolver back
// when the test ends.
func resetKnown(t *testing.T) {
	t.Helper()
	saved := resolver
	known.Lock()
	known.addrs, known.file = map[string][]string{}, ""
	known.Unlock()
	t.Cleanup(func() {
		resolver = saved
		known.Lock()
		known.addrs, known.file = map[string][]string{}, ""
		known.Unlock()
	})
}

// silentResolver is a resolver whose DNS server never answers.
func silentResolver() *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("no answer")
	}}
}

func listen(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return ln, port
}

// A name that doesn't resolve this time is dialled at its last known
// addresses; one never resolved fails as before.
func TestDialFallsBackToLastKnownAddresses(t *testing.T) {
	resetKnown(t)
	resolver = silentResolver()
	_, port := listen(t)

	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	defer cancel()
	if _, err := dialContext(ctx, "tcp", net.JoinHostPort("bridge.example.invalid", port)); err == nil {
		t.Fatal("a name never resolved must not dial")
	}

	remember("bridge.example.invalid", []string{"127.0.0.1"})
	c, err := dialContext(ctx, "tcp", net.JoinHostPort("bridge.example.invalid", port))
	if err != nil {
		t.Fatalf("dial at the last known address: %v", err)
	}
	c.Close()
}

// An address that doesn't answer gets its share of the time, not all of
// it: the next one is still tried.
func TestDialTriesEveryKnownAddress(t *testing.T) {
	resetKnown(t)
	resolver = silentResolver()
	_, port := listen(t)
	// 192.0.2.1 (TEST-NET-1) doesn't answer; sorted first.
	remember("bridge.example.invalid", []string{"192.0.2.1", "127.0.0.1"})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	c, err := dialContext(ctx, "tcp", net.JoinHostPort("bridge.example.invalid", port))
	if err != nil {
		t.Fatalf("the second address wasn't tried: %v", err)
	}
	c.Close()
}

// Addresses dialled directly never touch the resolver or the memory.
func TestDialLiteralAddress(t *testing.T) {
	resetKnown(t)
	resolver = silentResolver()
	_, port := listen(t)
	c, err := dialContext(context.Background(), "tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if n := len(lastKnown("127.0.0.1")); n != 0 {
		t.Fatalf("a literal address was remembered: %d", n)
	}
}

// The addresses survive a restart through the file, and a damaged file or
// entry is ignored.
func TestKeepAddressesInFile(t *testing.T) {
	resetKnown(t)
	path := filepath.Join(t.TempDir(), "addresses.json")
	KeepAddressesIn(path)
	remember("off-the.cloud", []string{"149.202.83.7", "37.187.141.41"})

	known.Lock()
	known.addrs = map[string][]string{}
	known.Unlock()
	KeepAddressesIn(path)
	got := lastKnown("off-the.cloud")
	if len(got) != 2 || got[0] != "149.202.83.7" || got[1] != "37.187.141.41" {
		t.Fatalf("after a restart: %v", got)
	}

	if err := os.WriteFile(path, []byte(`{"a.example":["not an address","10.0.0.1"],"b.example":["x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	known.Lock()
	known.addrs = map[string][]string{}
	known.Unlock()
	KeepAddressesIn(path)
	if got := lastKnown("a.example"); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Fatalf("bad entries kept: %v", got)
	}
	if got := lastKnown("b.example"); len(got) != 0 {
		t.Fatalf("a name with no valid address kept: %v", got)
	}

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	known.Lock()
	known.addrs = map[string][]string{}
	known.Unlock()
	KeepAddressesIn(path)
	if n := len(lastKnown("off-the.cloud")); n != 0 {
		t.Fatalf("a damaged file was read: %d", n)
	}
}

// Unchanged addresses aren't written again, and the number of names is
// bounded.
func TestRememberBounds(t *testing.T) {
	resetKnown(t)
	path := filepath.Join(t.TempDir(), "addresses.json")
	KeepAddressesIn(path)
	remember("off-the.cloud", []string{"37.187.141.41", "149.202.83.7"})
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	remember("off-the.cloud", []string{"149.202.83.7", "37.187.141.41"}) // same, another order
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the same addresses were written again")
	}
	for i := 0; i < cMaxNames+10; i++ {
		remember("n"+strconv.Itoa(i)+".example", []string{"10.0.0.1"})
	}
	known.Lock()
	n := len(known.addrs)
	known.Unlock()
	if n > cMaxNames {
		t.Fatalf("%d names kept, at most %d", n, cMaxNames)
	}
}
