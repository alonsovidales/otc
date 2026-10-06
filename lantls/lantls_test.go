// SPDX-License-Identifier: AGPL-3.0-or-later

package lantls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadOrCreateMakesAPairAndKeepsIt(t *testing.T) {
	storage := t.TempDir()

	first, err := LoadOrCreate(storage)
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	leaf := first.Cert.Leaf
	if leaf == nil {
		t.Fatal("expected the parsed leaf certificate")
	}
	if want := sha256.Sum256(first.Cert.Certificate[0]); !bytes.Equal(first.Pin, want[:]) {
		t.Error("the pin must be the SHA-256 of the certificate's DER bytes")
	}
	if leaf.Subject.CommonName != "otc-lan" {
		t.Errorf("CN = %q, want otc-lan", leaf.Subject.CommonName)
	}
	if years := leaf.NotAfter.Sub(time.Now()).Hours() / 24 / 365; years < 99 {
		t.Errorf("valid for %.1f years, want 100", years)
	}
	key, ok := first.Cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		t.Errorf("key is %T, want an ECDSA P-256 key", first.Cert.PrivateKey)
	}

	// Every later start loads the same pair: the apps hold its pin.
	second, err := LoadOrCreate(storage)
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if !bytes.Equal(first.Pin, second.Pin) {
		t.Error("a second start changed the pin")
	}
}

func TestLoadOrCreateKeepsTheKeyPrivate(t *testing.T) {
	storage := t.TempDir()
	if _, err := LoadOrCreate(storage); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(storage, cDir)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v, want a 0700 directory", info.Mode())
	}
	for _, name := range []string{cKeyFile, cCertFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", name, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(dir + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the temporary folder was left behind")
	}
}

// A pair that can't be read is never replaced by a new one: that would
// change the pin under every app that holds it.
func TestLoadOrCreateRefusesABrokenPairWithoutReplacingIt(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(dir string) error
	}{
		{"corrupt certificate", func(dir string) error {
			return os.WriteFile(filepath.Join(dir, cCertFile), []byte("not a certificate"), 0o600)
		}},
		{"missing key", func(dir string) error {
			return os.Remove(filepath.Join(dir, cKeyFile))
		}},
		{"unreadable folder", func(dir string) error {
			if os.Geteuid() == 0 {
				t.Skip("root reads it anyway")
			}
			return os.Chmod(dir, 0)
		}},
	}
	snapshot := func(dir string) map[string]string {
		out := map[string]string{}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			out[e.Name()] = string(b)
		}
		return out
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			storage := t.TempDir()
			if _, err := LoadOrCreate(storage); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(storage, cDir)
			t.Cleanup(func() { os.Chmod(dir, 0o700) })
			if c.name != "unreadable folder" {
				if err := c.breakIt(dir); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(dir)
			if c.name == "unreadable folder" {
				if err := c.breakIt(dir); err != nil {
					t.Fatal(err)
				}
			}

			if id, err := LoadOrCreate(storage); err == nil {
				t.Fatalf("expected an error, got an identity with pin %x", id.Pin)
			}

			os.Chmod(dir, 0o700)
			if after := snapshot(dir); !reflect.DeepEqual(before, after) {
				t.Error("the broken pair was changed")
			}
		})
	}
}

func TestLoadOrCreateIgnoresAnInterruptedFirstStart(t *testing.T) {
	storage := t.TempDir()
	tmp := filepath.Join(storage, cDir+".new")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, cKeyFile), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(storage); err != nil {
		t.Fatalf("a leftover temporary folder must not stop the first start: %v", err)
	}
}

func TestLoadOrCreateNeedsAStoragePath(t *testing.T) {
	if _, err := LoadOrCreate(""); err == nil {
		t.Error("expected an error without a storage path")
	}
}

func TestPort(t *testing.T) {
	cases := []struct {
		configured string
		http       int
		want       int
		ok         bool
	}{
		{"", 8080, 8443, true},
		{"", 8081, 8444, true},
		{" 9443 ", 8080, 9443, true},
		{"", 0, 0, false},
		{"", 65300, 0, false},
		{"abc", 8080, 0, false},
		{"0", 8080, 0, false},
		{"70000", 8080, 0, false},
	}
	for _, c := range cases {
		got, err := Port(c.configured, c.http)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("Port(%q, %d) = %d, %v; want %d (ok %v)", c.configured, c.http, got, err, c.want, c.ok)
		}
	}
}

func TestEndpointServingState(t *testing.T) {
	var none *Endpoint
	if none.Serving() {
		t.Error("no listener can't be serving")
	}
	pin := []byte{1, 2, 3}
	ep := NewEndpoint(8443, pin)
	pin[0] = 9
	if !ep.Serving() || ep.Port() != 8443 || !bytes.Equal(ep.Pin(), []byte{1, 2, 3}) {
		t.Errorf("got serving %v port %d pin %v", ep.Serving(), ep.Port(), ep.Pin())
	}
	ep.Pin()[0] = 9
	if ep.Pin()[0] != 1 {
		t.Error("a caller changed the endpoint's pin")
	}
	ep.Stop()
	if ep.Serving() {
		t.Error("a stopped listener is not serving")
	}
}

func ipNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip
	return n
}

func TestFilterAddresses(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast | net.FlagMulticast
	n := func(cidrs ...string) []net.Addr {
		var out []net.Addr
		for _, c := range cidrs {
			out = append(out, ipNet(t, c))
		}
		return out
	}
	ifs := []iface{
		{"lo", net.FlagUp | net.FlagLoopback, n("127.0.0.1/8", "::1/128")},
		{"eth0", up, n("192.168.1.10/24", "fe80::1/64", "fd12:3456::10/64", "2a01:4f8::1/64", "fd7a:115c:a1e0::5/128")},
		{"wlan0", up, n("10.0.0.5/24", "10.42.0.7/24", "169.254.3.4/16", "8.8.8.8/32")},
		{"uap0", up, n("10.42.0.1/24")},
		{"tailscale0", up, n("100.101.102.103/32", "fd7a:115c:a1e0::1/128")},
		{"docker0", up, n("172.17.0.1/16")},
		{"br-3f2a", up, n("172.18.0.1/16")},
		{"veth9a1", up, n("172.19.0.1/16")},
		{"eth1", net.FlagBroadcast, n("192.168.2.10/24")},
		{"usb0", up, []net.Addr{&net.IPAddr{IP: net.ParseIP("172.20.0.5")}}},
		{"eth0.5", up, n("192.168.1.10/24")},
	}

	got := filterAddresses(ifs)

	want := []string{"192.168.1.10", "10.0.0.5", "172.20.0.5", "fd12:3456::10"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAddressesReadsTheInterfaces(t *testing.T) {
	saved := interfaces
	t.Cleanup(func() { interfaces = saved })

	interfaces = func() ([]iface, error) {
		return []iface{{"eth0", net.FlagUp, []net.Addr{ipNet(t, "192.168.1.10/24")}}}, nil
	}
	if got, err := Addresses(); err != nil || !reflect.DeepEqual(got, []string{"192.168.1.10"}) {
		t.Errorf("got %v, %v", got, err)
	}

	interfaces = func() ([]iface, error) { return nil, errors.New("netlink") }
	if _, err := Addresses(); err == nil {
		t.Error("expected the listing's error")
	}
}
