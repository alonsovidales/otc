// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/limits"
	gorilla "github.com/gorilla/websocket"
)

// A device's address is one label under the bridge's domain; anything
// else anyone puts in a Host header is no device's.
func TestIsDeviceHost(t *testing.T) {
	for host, want := range map[string]bool{
		"casa.off-the.cloud":     true,
		"Casa.Off-The.Cloud":     true,
		"my_pi.off-the.cloud":    true,
		"a.off-the.cloud":        true,
		"off-the.cloud":          false,
		"www.casa.off-the.cloud": false,
		"casa.off-the.cloud:443": false,
		"-casa.off-the.cloud":    false,
		"casa.example.com":       false,
		"198.51.100.20":          false,
		"[2001:db8::1]":          false,
		"":                       false,
		"casa.off-the.cloud.":    false,
		strings.Repeat("x", 63) + ".off-the.cloud": true,
		strings.Repeat("x", 64) + ".off-the.cloud": false,
	} {
		if got := IsDeviceHost(host, "off-the.cloud"); got != want {
			t.Errorf("IsDeviceHost(%q) = %v, want %v", host, got, want)
		}
	}
	if !IsDeviceHost("127.0.0.1:5555", "") {
		t.Error("with no domain configured, a host must still be able to be a device's")
	}
}

// The disabled flag is read from the database once in cDisabledTTL, never
// for an address that can't be a device's, and a change made here shows
// at once.
func TestTheDisabledFlagIsRememberedAndAskedOnlyForDevices(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").WithArgs("casa.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(true))
	mg := &Manager{dao: dao.NewWithDB(db), tld: "off-the.cloud"}

	for i := 0; i < 5; i++ {
		if disabled, err := mg.DeviceDisabled("203.0.113.7", "casa.off-the.cloud"); err != nil || !disabled {
			t.Fatalf("read %d: %v %v", i, disabled, err)
		}
	}
	for _, host := range []string{"1.2.3.4", "evil.example", "a.b.off-the.cloud", "casa.off-the.cloud:8443"} {
		if disabled, err := mg.DeviceDisabled("203.0.113.7", host); err != nil || disabled {
			t.Errorf("%s: %v %v", host, disabled, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	mg.rememberDisabled("casa.off-the.cloud", false, time.Now())
	if disabled, _ := mg.DeviceDisabled("203.0.113.7", "casa.off-the.cloud"); disabled {
		t.Error("a flag set through this node didn't show at once")
	}

	// Past cDisabledTTL it is read again.
	setVar(t, &cDisabledTTL, time.Millisecond)
	mg.rememberDisabled("casa.off-the.cloud", false, time.Now())
	time.Sleep(5 * time.Millisecond)
	mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").WithArgs("casa.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(true))
	if disabled, _ := mg.DeviceDisabled("203.0.113.7", "casa.off-the.cloud"); !disabled {
		t.Error("an expired flag was not read again")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// One address asking about name after name is looked up only so often;
// the others' answers don't wait on it.
func TestDisabledLookupsAreLimitedPerAddress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, d := range []string{"a.off-the.cloud", "b.off-the.cloud", "c.off-the.cloud"} {
		mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").WithArgs(d).
			WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))
	}
	mg := &Manager{dao: dao.NewWithDB(db), tld: "off-the.cloud", lookupPerAddr: limits.NewRate(0.001, 2)}
	for _, q := range []struct{ addr, host string }{
		{"198.51.100.7", "a.off-the.cloud"},
		{"198.51.100.7", "b.off-the.cloud"},
		{"198.51.100.7", "x.off-the.cloud"}, // past the address's lookups: not asked
		{"203.0.113.9", "c.off-the.cloud"},
	} {
		// A query the mock doesn't expect is an error.
		if _, err := mg.DeviceDisabled(q.addr, q.host); err != nil {
			t.Errorf("%s from %s: %v", q.host, q.addr, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A socket that keeps asking for a device it can't have is closed after
// cUnpairedMaxRefusals answers, and asks the database once for them all.
func TestASocketThatKeepsAskingForAnUnreachableDeviceIsClosed(t *testing.T) {
	setVar(t, &cUnpairedMaxRefusals, 3)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").
			WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(false))
	}
	mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
	dial, _ := newTestBridge(t, mg)
	c := dial()
	for i := int32(1); i <= 3; i++ {
		if err := c.WriteMessage(gorilla.BinaryMessage, envelopeFrame(t, i)); err != nil {
			t.Fatal(err)
		}
		if ask := readResp(t, c); ask.GetRespAck().GetCode() != cCodeDeviceUnreachable {
			t.Fatalf("answer %d: %v", i, ask)
		}
	}
	if !closedSoon(c, 2*time.Second) {
		t.Fatal("the socket stayed open")
	}
	// One lookup for all three: two of the three expected are left.
	if err := mock.ExpectationsWereMet(); err == nil {
		t.Error("every message of the socket was looked up")
	}
}
