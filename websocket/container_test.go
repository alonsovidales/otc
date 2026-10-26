// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/updater"
)

const vaultCount = "select count\\(\\*\\) from `vault`"

// inContainer turns container mode on for the test, with the first
// password's window open for another 10 minutes.
func inContainer(t *testing.T) {
	t.Helper()
	t.Setenv("OTC_CONTAINER", "1")
	t.Setenv("OTC_SETUP_UNTIL", strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10))
}

func mockDB(t *testing.T) (*dao.Dao, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return dao.NewWithDB(db), mock
}

const (
	homeOnly     = "The owner password can only be chosen from your home network, at the device's own address and not through a proxy. You can also set it on the server: docker exec -i otc otc-entrypoint set-password, with the password as its input."
	windowClosed = "The 30 minutes to choose the owner password here have passed. Set it on the server instead: docker exec -i otc otc-entrypoint set-password, with the password as its input."
)

// The first owner password of a container's primary instance is taken
// only from the home network, straight to its own port, within its window.
func TestContainerFirstPassword(t *testing.T) {
	now := time.Now()
	future := strconv.FormatInt(now.Add(time.Minute).Unix(), 10)
	past := strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)
	for _, c := range []struct {
		name       string
		peer       string
		proxied    bool
		fromBridge bool
		until      string
		want       string // "" allowed
	}{
		{"home address", "192.168.1.20", false, false, future, ""},
		{"loopback", "127.0.0.1", false, false, future, ""},
		{"IPv6 unique local", "fd00::20", false, false, future, ""},
		{"IPv4-mapped home address", "::ffff:192.168.1.20", false, false, future, ""},
		{"Tailscale / CGNAT", "100.100.1.2", false, false, future, ""},
		{"proxied, from a home address", "172.18.0.2", true, false, future, homeOnly},
		{"proxied, from loopback", "127.0.0.1", true, false, future, homeOnly},
		{"a public address", "203.0.113.9", false, false, future, homeOnly},
		{"IPv4-mapped public address", "::ffff:203.0.113.9", false, false, future, homeOnly},
		{"a public IPv6 address", "2001:db8::9", false, false, future, homeOnly},
		{"no peer", "", false, false, future, homeOnly},
		{"through the bridge", "", false, true, future, homeOnly},
		{"window passed", "192.168.1.20", false, false, past, windowClosed},
		{"window missing", "192.168.1.20", false, false, "", windowClosed},
		{"window malformed", "192.168.1.20", false, false, "in a while", windowClosed},
		{"window too far ahead", "192.168.1.20", false, false, strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10), windowClosed},
		{"a public address after the window", "203.0.113.9", false, false, past, homeOnly},
	} {
		t.Run(c.name, func(t *testing.T) {
			inContainer(t)
			t.Setenv("OTC_SETUP_UNTIL", c.until)
			d, mock := mockDB(t)
			mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
			ch := &connHandler{mg: &Manager{dao: d, sup: &supervisor.Supervisor{}}, peer: c.peer, proxied: c.proxied, fromBridge: c.fromBridge}
			msg, refused := ch.firstPasswordRefusal(now)
			got := ""
			if refused {
				got = msg.Render("")
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}

// With the owner password set, a sign-in goes on as usual from anywhere.
// When the database can't say whether one is set, the sign-in is refused:
// it would read the same table again, and could choose the first password
// unchecked.
func TestContainerFirstPasswordOnlyBeforeOneIsSet(t *testing.T) {
	inContainer(t)
	t.Setenv("OTC_SETUP_UNTIL", "")
	auth := &pb.ReqEnvelope{Id: 9, Payload: &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Create: true}}}

	d, mock := mockDB(t)
	ch := &connHandler{mg: &Manager{dao: d, sup: &supervisor.Supervisor{}}, peer: "203.0.113.9", proxied: true}
	mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if resp, handled := ch.containerGate(auth, ""); handled || resp != nil {
		t.Errorf("password set: %+v", resp)
	}
	mock.ExpectQuery(vaultCount).WillReturnError(errors.New("gone"))
	resp, handled := ch.containerGate(auth, "")
	if ack := resp.GetRespAck(); !handled || resp.Id != 9 || ack == nil || ack.Ok || ack.ErrorMsg != "error checking device state" {
		t.Errorf("database error: %+v", resp)
	}

	// The same for an additional user's instance.
	t.Setenv(supervisor.ChildEnvVar, "4b0e")
	child := &connHandler{mg: &Manager{dao: d}, peer: "203.0.113.9", proxied: true}
	mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if resp, handled := child.containerGate(auth, ""); handled || resp != nil {
		t.Errorf("additional user's instance, password set: %+v", resp)
	}
	mock.ExpectQuery(vaultCount).WillReturnError(errors.New("gone"))
	if resp, handled := child.containerGate(auth, ""); !handled || resp.GetRespAck().GetErrorMsg() != "error checking device state" {
		t.Errorf("additional user's instance, database error: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

const userHomeOnly = "This account's first password can only be chosen from your home network, at the account's own address and not through a proxy, or at its address on the bridge."

// An additional user's instance takes its first password through the
// bridge, or straight from the home network, at any time; its ports are
// the server's, so not from anywhere else. It is told by its own
// environment, not by having no supervisor.
func TestContainerChildFirstPassword(t *testing.T) {
	past := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	for _, c := range []struct {
		name       string
		peer       string
		proxied    bool
		fromBridge bool
		want       string
	}{
		{"through the bridge", "", false, true, ""},
		{"home address", "192.168.1.20", false, false, ""},
		{"loopback", "127.0.0.1", false, false, ""},
		{"a public address", "203.0.113.9", false, false, userHomeOnly},
		{"proxied, from a home address", "172.18.0.2", true, false, userHomeOnly},
		{"proxied, from a public address", "203.0.113.9", true, false, userHomeOnly},
		{"no peer", "", false, false, userHomeOnly},
	} {
		t.Run(c.name, func(t *testing.T) {
			inContainer(t)
			t.Setenv("OTC_SETUP_UNTIL", past)
			t.Setenv(supervisor.ChildEnvVar, "4b0e")
			d, mock := mockDB(t)
			mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
			// A supervisor or none: the environment decides.
			for _, sup := range []*supervisor.Supervisor{nil, {}} {
				ch := &connHandler{mg: &Manager{dao: d, sup: sup}, peer: c.peer, proxied: c.proxied, fromBridge: c.fromBridge}
				msg, refused := ch.firstPasswordRefusal(time.Now())
				got := ""
				if refused {
					got = msg.Render("")
				}
				if got != c.want {
					t.Errorf("supervisor %v: got %q, want %q", sup != nil, got, c.want)
				}
				if sup == nil {
					mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}

	// The primary without its supervisor (os.Executable failed) is still
	// the primary: its window applies.
	inContainer(t)
	t.Setenv("OTC_SETUP_UNTIL", past)
	d, mock := mockDB(t)
	mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	ch := &connHandler{mg: &Manager{dao: d}, peer: "192.168.1.20"}
	if msg, refused := ch.firstPasswordRefusal(time.Now()); !refused || msg.Render("") != windowClosed {
		t.Errorf("primary with no supervisor: %v %q", refused, msg.Render(""))
	}
}

// Through processMessage: the refusal is an Ack in the request's language,
// with no code, and no session is made.
func TestContainerFirstPasswordRefusedInTheRequestsLanguage(t *testing.T) {
	inContainer(t)
	d, mock := mockDB(t)
	mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	ch := &connHandler{mg: &Manager{dao: d, sup: &supervisor.Supervisor{}}, peer: "203.0.113.9"}
	resp, closeConn := ch.processMessage(&pb.ReqEnvelope{Id: 5, Lang: "es", Payload: &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Create: true}}})
	ack := resp.GetRespAck()
	if closeConn || resp.Id != 5 || ack == nil || ack.Ok || ack.Code != "" ||
		ack.ErrorMsg != "La contraseña del propietario solo se puede elegir desde tu red doméstica, en la dirección propia del dispositivo y sin pasar por un proxy. También puedes establecerla en el servidor: docker exec -i otc otc-entrypoint set-password, con la contraseña como entrada." {
		t.Errorf("%+v", resp)
	}
	if ch.getSession() != nil {
		t.Error("a session was made")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The machine's disks and Wi-Fi are the server's: nothing listed, an
// empty choice accepted, a real one refused - for a signed-in owner; a
// connection without a session gets the usual answer.
func TestContainerGateMachineRequests(t *testing.T) {
	inContainer(t)
	ch := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}
	listDisks := &pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqListStorageDevices{ReqListStorageDevices: &pb.ListStorageDevices{}}}
	if resp, handled := ch.containerGate(listDisks, ""); handled || resp != nil {
		t.Errorf("no session: %+v", resp)
	}

	ch.setSession(newTestAuthenticatedSession(t))
	resp, handled := ch.containerGate(listDisks, "")
	if !handled || resp.Id != 3 || resp.Error || resp.GetRespStorageDevices() == nil || len(resp.GetRespStorageDevices().Devices) != 0 {
		t.Errorf("disks: %+v", resp)
	}
	resp, handled = ch.containerGate(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqListWifiNetworks{ReqListWifiNetworks: &pb.ListWifiNetworks{}}}, "")
	if !handled || resp.Error || resp.GetRespWifiNetworks() == nil || len(resp.GetRespWifiNetworks().Networks) != 0 {
		t.Errorf("networks: %+v", resp)
	}
	resp, handled = ch.containerGate(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqSetupStorage{ReqSetupStorage: &pb.SetupStorage{}}}, "")
	if !handled || resp.Error || !resp.GetRespAck().GetOk() {
		t.Errorf("no disks chosen: %+v", resp)
	}
	const refused = "Not available in Docker: the server the device runs on looks after its disks and network."
	resp, handled = ch.containerGate(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqSetupStorage{ReqSetupStorage: &pb.SetupStorage{DevicePaths: []string{"/dev/sda"}}}}, "")
	if !handled || !resp.Error || resp.ErrorMessage != refused {
		t.Errorf("disks chosen: %+v", resp)
	}
	resp, handled = ch.containerGate(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqJoinWifi{ReqJoinWifi: &pb.JoinWifi{Ssid: "home"}}}, "")
	if !handled || !resp.Error || resp.ErrorMessage != refused {
		t.Errorf("Wi-Fi: %+v", resp)
	}
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 3, Lang: "de", Payload: &pb.ReqEnvelope_ReqJoinWifi{ReqJoinWifi: &pb.JoinWifi{Ssid: "home"}}})
	if !resp.Error || resp.ErrorMessage != "In Docker nicht verfügbar: Um Laufwerke und Netzwerk kümmert sich der Server, auf dem das Gerät läuft." {
		t.Errorf("Wi-Fi, in German: %+v", resp)
	}
	resp, handled = ch.containerGate(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqGetWifiStatus{ReqGetWifiStatus: &pb.GetWifiStatus{}}}, "")
	if !handled || resp.Error || resp.GetRespWifiStatus() == nil || resp.GetRespWifiStatus().Managed {
		t.Errorf("Wi-Fi status: %+v", resp)
	}
	if resp, handled := ch.containerGate(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{}}}, ""); handled || resp != nil {
		t.Errorf("anything else: %+v", resp)
	}
}

// Without OTC_CONTAINER nothing of this runs: the storage request reaches
// its usual handler (an additional user's instance refuses it as before).
func TestContainerGateOffElsewhere(t *testing.T) {
	t.Setenv("OTC_CONTAINER", "")
	ch := &connHandler{mg: &Manager{}}
	ch.setSession(newTestAuthenticatedSession(t))
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqJoinWifi{ReqJoinWifi: &pb.JoinWifi{Ssid: "home"}}})
	if resp == nil || !resp.Error || resp.ErrorMessage != "not available on this instance" {
		t.Errorf("%+v", resp)
	}
}

// The mirror's disks are watched on a Raspberry Pi, and in a container
// only when it has a mirror of its own (docker/compose.mirror.yaml).
func TestMirrorWatched(t *testing.T) {
	for _, c := range []struct {
		container, array string
		want             bool
	}{
		{"", "", true},
		{"", "md127", true},
		{"1", "", false},
		{"1", "md127", true},
		{"1", "sda", false},
	} {
		t.Setenv("OTC_CONTAINER", c.container)
		t.Setenv("OTC_MIRROR_ARRAY", c.array)
		if got := mirrorWatched(); got != c.want {
			t.Errorf("OTC_CONTAINER=%q OTC_MIRROR_ARRAY=%q: %v, want %v", c.container, c.array, got, c.want)
		}
	}
}

// GetPubKey tells the first-run setup it is a container's.
func TestPubKeySaysContainer(t *testing.T) {
	for _, on := range []string{"1", ""} {
		t.Setenv("OTC_CONTAINER", on)
		d, mock := mockDB(t)
		mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		ch := &connHandler{mg: &Manager{dao: d, sup: &supervisor.Supervisor{}}}
		resp, _ := ch.processNonAuthRequest(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}}, "")
		key := resp.GetRespPubKey()
		if key == nil || key.Container != (on == "1") || !key.IsPrimary || !key.IsNewDevice {
			t.Errorf("OTC_CONTAINER=%q: %+v", on, resp)
		}
	}
}

func TestProxyHeaders(t *testing.T) {
	for _, h := range []string{
		"X-Forwarded-For", "Forwarded", "X-Real-IP", "X-Forwarded-Host", "X-Forwarded-Proto",
		"Via", "CF-Connecting-IP", "True-Client-IP",
	} {
		r, _ := http.NewRequest(http.MethodGet, "/ws", nil)
		r.Header.Set(h, "")
		if !proxyHeaders(r) {
			t.Errorf("%s: not seen", h)
		}
	}
	r, _ := http.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("User-Agent", "x")
	if proxyHeaders(r) {
		t.Error("no proxy header: seen")
	}
}

// A browser's socket from a page that isn't the listener's own - another
// site, or the device's page behind a TLS proxy that adds no headers - is
// not straight from the device's page.
func TestForeignOrigin(t *testing.T) {
	for _, c := range []struct {
		name, host, origin string
		tls                bool
		want               bool
	}{
		{"no Origin (the apps)", "192.168.1.20:8080", "", false, false},
		{"its own page", "192.168.1.20:8080", "http://192.168.1.20:8080", false, false},
		{"its own page, other case", "OTC.local:8080", "http://otc.local:8080", false, false},
		{"its own page, IPv6", "[fd00::20]:8080", "http://[fd00::20]:8080", false, false},
		{"its own page on the TLS port", "192.168.1.20:8443", "https://192.168.1.20:8443", true, false},
		{"another site", "192.168.1.20:8080", "https://evil.example", false, true},
		{"another site, plain", "192.168.1.20:8080", "http://evil.example", false, true},
		{"another port", "192.168.1.20:8080", "http://192.168.1.20:9000", false, true},
		{"https in front of the plain port, Host passed on", "photos.example.com", "https://photos.example.com", false, true},
		{"https in front of the plain port, Host not passed on", "127.0.0.1:8080", "https://photos.example.com", false, true},
		{"http page on the TLS port", "192.168.1.20:8443", "http://192.168.1.20:8443", true, true},
		{"a sandboxed or local page", "192.168.1.20:8080", "null", false, true},
		{"not a URL", "192.168.1.20:8080", "http://[::1", false, true},
		{"an app's own scheme", "192.168.1.20:8080", "app://local", false, false},
	} {
		r, _ := http.NewRequest(http.MethodGet, "/ws", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := foreignOrigin(r); got != c.want {
			t.Errorf("%s: foreign %v, want %v", c.name, got, c.want)
		}
		if got := indirect(r); got != c.want {
			t.Errorf("%s: indirect %v, want %v", c.name, got, c.want)
		}
	}
	r, _ := http.NewRequest(http.MethodGet, "/ws", nil)
	r.Host = "192.168.1.20:8080"
	r.Header.Set("Origin", "http://192.168.1.20:8080")
	r.Header.Set("Via", "1.1 proxy")
	if !indirect(r) {
		t.Error("its own page through a proxy: not indirect")
	}
}

func TestPeerHost(t *testing.T) {
	for addr, want := range map[string]string{
		"192.168.1.5:51000":    "192.168.1.5",
		"[fd00::5]:51000":      "fd00::5",
		"[::ffff:10.0.0.1]:80": "::ffff:10.0.0.1",
		"odd":                  "odd",
	} {
		if got := peerHost(&http.Request{RemoteAddr: addr}); got != want {
			t.Errorf("%q: %q, want %q", addr, got, want)
		}
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got := parseTrustedProxies(" 172.18.0.0/16, 10.0.0.5 fd00::/8,bogus,10.1.2.3/40,::ffff:192.168.0.0/112")
	want := []netip.Prefix{
		netip.MustParsePrefix("172.18.0.0/16"),
		netip.MustParsePrefix("10.0.0.5/32"),
		netip.MustParsePrefix("fd00::/8"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := parseTrustedProxies(""); got != nil {
		t.Errorf("empty: %v", got)
	}
}

// X-Forwarded-For is believed only from a trusted proxy, read from the
// right: the left of the header is the client's own to write.
func TestForwardedClient(t *testing.T) {
	trusted := parseTrustedProxies("172.18.0.0/16")
	for _, c := range []struct {
		name string
		peer string
		xff  []string
		want string
	}{
		{"from the proxy", "172.18.0.2", []string{"203.0.113.7"}, "203.0.113.7"},
		{"a client's own header comes first", "172.18.0.2", []string{"6.6.6.6, 203.0.113.7"}, "203.0.113.7"},
		{"two proxies of ours", "172.18.0.2", []string{"203.0.113.7, 172.18.0.3"}, "203.0.113.7"},
		{"several lines", "172.18.0.2", []string{"6.6.6.6", "203.0.113.7"}, "203.0.113.7"},
		{"with a port", "172.18.0.2", []string{"203.0.113.7:4000"}, "203.0.113.7"},
		{"IPv6", "172.18.0.2", []string{"2001:db8::7"}, "2001:db8::7"},
		{"IPv4-mapped", "::ffff:172.18.0.2", []string{"::ffff:203.0.113.7"}, "203.0.113.7"},
		{"only proxies of ours", "172.18.0.2", []string{"172.18.0.9, 172.18.0.3"}, "172.18.0.9"},
		{"not an address", "172.18.0.2", []string{"203.0.113.7, unknown"}, "172.18.0.2"},
		{"no header", "172.18.0.2", nil, "172.18.0.2"},
		{"from someone else", "192.168.1.9", []string{"203.0.113.7"}, "192.168.1.9"},
		{"loopback is not trusted by itself", "127.0.0.1", []string{"203.0.113.7"}, "127.0.0.1"},
	} {
		if got := forwardedClient(c.peer, c.xff, trusted); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := forwardedClient("127.0.0.1", []string{"203.0.113.7"}, nil); got != "127.0.0.1" {
		t.Errorf("nothing trusted: %q", got)
	}
}

// clientAddr in a container follows trusted-proxies, not loopback;
// elsewhere loopback's header is believed as before (Tailscale Funnel).
func TestClientAddrInAContainer(t *testing.T) {
	saved := trustedProxies
	trustedProxies = func() []netip.Prefix { return parseTrustedProxies("10.0.0.0/8") }
	t.Cleanup(func() { trustedProxies = saved })
	req := func(remote, xff string) *http.Request {
		r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	t.Setenv("OTC_CONTAINER", "1")
	if got := clientAddr(req("127.0.0.1:5000", "203.0.113.7")); got != "127.0.0.1" {
		t.Errorf("in a container, from loopback: %q", got)
	}
	if got := clientAddr(req("10.0.0.2:5000", "203.0.113.7")); got != "203.0.113.7" {
		t.Errorf("in a container, from a trusted proxy: %q", got)
	}

	t.Setenv("OTC_CONTAINER", "")
	if got := clientAddr(req("127.0.0.1:5000", "203.0.113.7")); got != "203.0.113.7" {
		t.Errorf("elsewhere, from loopback: %q", got)
	}
	if got := clientAddr(req("10.0.0.2:5000", "203.0.113.7")); got != "10.0.0.2" {
		t.Errorf("elsewhere, from the LAN: %q", got)
	}
}

// A user's port and the TLS port beside it are free on the server, none
// of the primary's, and no other user's.
func TestUserPortUsable(t *testing.T) {
	users := map[int]bool{18081: true}
	inUse := func(p int) (bool, error) { return users[p], nil }
	busy := map[int]bool{18083: true, 18447: true}
	free := func(p int) bool { return !busy[p] }
	reserved := []int{18080, 18443, 3316}
	for p, want := range map[int]bool{
		18082:       true,
		18080:       false, // the primary's HTTP port
		18443:       false, // the primary's TLS port
		18080 - 363: false, // its TLS port is the primary's HTTP port
		3316:        false, // the database
		18081:       false, // a user's
		18444:       false, // 18081's TLS port
		17718:       false, // its TLS port is 18081, a user's HTTP port
		18083:       false, // busy on the server
		18084:       false, // its TLS port (18447) busy on the server
		0:           false,
		65300:       false, // no TLS port beside it
	} {
		got, err := userPortUsable(p, reserved, inUse, free)
		if err != nil || got != want {
			t.Errorf("port %d: %v %v, want %v", p, got, err, want)
		}
	}
	if _, err := userPortUsable(18082, reserved, func(int) (bool, error) { return false, errors.New("gone") }, free); err == nil {
		t.Error("a database error was not returned")
	}
}

func TestNextUserPort(t *testing.T) {
	p, err := nextUserPort(18081, func(p int) (bool, error) { return p >= 18085, nil })
	if err != nil || p != 18085 {
		t.Errorf("%d %v", p, err)
	}
	if _, err := nextUserPort(18081, func(int) (bool, error) { return false, nil }); err == nil {
		t.Error("no free port: no error")
	}
	if _, err := nextUserPort(18081, func(int) (bool, error) { return false, errors.New("gone") }); err == nil {
		t.Error("a database error was not returned")
	}
}

// A new user's port in a container counts from the primary's HTTP port
// and skips what the server already holds; a port asked for is checked
// the same way.
func TestContainerUserPort(t *testing.T) {
	inContainer(t)
	savedFree := portFree
	portFree = func(p int) bool { return p != 8081 }
	t.Cleanup(func() { portFree = savedFree })
	d, mock := mockDB(t)
	ch := &connHandler{mg: &Manager{dao: d, sup: &supervisor.Supervisor{}}}

	portInUse := "select count\\(\\*\\) from `users` where `port` = \\?"
	none := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"n"}).AddRow(0) }
	mock.ExpectQuery("select max\\(`port`\\) from `users`").WillReturnRows(sqlmock.NewRows([]string{"m"}).AddRow(nil))
	for _, q := range []int{8081, 8444, 8081 - 363} { // 8081: busy on the server
		mock.ExpectQuery(portInUse).WithArgs(q).WillReturnRows(none())
	}
	for _, q := range []int{8082, 8445, 8082 - 363} {
		mock.ExpectQuery(portInUse).WithArgs(q).WillReturnRows(none())
	}
	if p, err := ch.containerUserPort(0); err != nil || p != 8082 {
		t.Errorf("next: %d %v", p, err)
	}

	for _, q := range []int{8081, 8444, 8081 - 363} {
		mock.ExpectQuery(portInUse).WithArgs(q).WillReturnRows(none())
	}
	if _, err := ch.containerUserPort(8081); err == nil || err.Error() != "port 8081 is already in use by another user" {
		t.Errorf("asked for a busy port: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestContainerUpdateInfo(t *testing.T) {
	out := &pb.RespUpdateInfo{State: "idle"}
	containerUpdateInfo(out, "fr")
	if !out.Container || out.Message != "Cet appareil fonctionne dans Docker et se met à jour avec son image : sur son serveur, exécutez docker compose pull puis docker compose up -d." {
		t.Errorf("%+v", out)
	}
	out = &pb.RespUpdateInfo{State: "failed", Message: "the build failed"}
	containerUpdateInfo(out, "")
	if !out.Container || out.Message != "the build failed" {
		t.Errorf("a status message is kept: %+v", out)
	}
}

// In a container the update alert waits for the release's image, and it
// and its push say how a container is updated.
func TestUpdateAlertInAContainer(t *testing.T) {
	inContainer(t)
	published := map[int]bool{130: true}
	saved := imageAvailable
	imageAvailable = func(v int) (bool, error) {
		if v == 132 {
			return false, errors.New("unreachable")
		}
		return published[v], nil
	}
	t.Cleanup(func() { imageAvailable = saved })
	d, mock := mockDB(t)
	var pushed []string
	notify := func(text push.Text, _ push.Target) {
		title, body := text("")
		pushed = append(pushed, title+" | "+body)
	}

	// Not published, or the registry can't say: nothing stored, nothing
	// pushed.
	updateAlert(d, notify, &updater.Alert{Level: updater.KindCritical, Version: "4.1", Target: 131, Summary: "x"})
	updateAlert(d, notify, &updater.Alert{Level: updater.KindCritical, Version: "4.2", Target: 132, Summary: "x"})

	const critical4 = "Critical update 4.0 - please install it soon"
	mock.ExpectQuery(updateAlertCount).WithArgs("4.0", "Update 4.0 is available", critical4).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `notifications`.*`update_version`").
		WithArgs(sqlmock.AnyArg(), critical4, "A fix. To install it, run docker compose pull and then docker compose up -d on the device's server.", "4.0", "dev.alert.update.critical", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	updateAlert(d, notify, &updater.Alert{Level: updater.KindCritical, Version: "4.0", Target: 130, Summary: "A fix."})
	if len(pushed) != 1 || pushed[0] != critical4+" | A fix. To install it, run docker compose pull and docker compose up -d on your server." {
		t.Errorf("pushed %q", pushed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestContainerLocalURL(t *testing.T) {
	if got := containerLocalURL(func() ([]string, error) { return []string{"192.168.1.20", "fd00::20"}, nil }); got != "http://192.168.1.20:8080" {
		t.Errorf("%q", got)
	}
	if got := containerLocalURL(func() ([]string, error) { return []string{"fd00::20"}, nil }); got != "http://[fd00::20]:8080" {
		t.Errorf("IPv6: %q", got)
	}
	if got := containerLocalURL(func() ([]string, error) { return nil, errors.New("netlink") }); got != "http://localhost:8080" {
		t.Errorf("no address: %q", got)
	}
}
