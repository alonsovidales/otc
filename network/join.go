// SPDX-License-Identifier: AGPL-3.0-or-later

package network

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// The rules a change must meet - the same in the runner and the three
// apps: a name of 1-32 bytes of UTF-8 without control characters, and a
// password that is empty (an open network), 8-63 bytes of UTF-8 without
// control characters, or exactly 64 hexadecimal digits.
const (
	cMaxSSIDBytes   = 32
	cMinPSKBytes    = 8
	cMaxPSKBytes    = 63
	cHexPSKBytes    = 64
	cMaxRequestSize = 1024
)

// The rate limit: at most cMaxJoins changes handed over in cJoinWindow.
// The runner keeps its own, which holds even against this process.
const (
	cMaxJoins   = 6
	cJoinWindow = 15 * time.Minute
)

// Refusal is a change refused before anything was asked of the runner:
// Code is RespEnvelope.error_code, Msg its text.
type Refusal struct {
	Code string
	Msg  i18n.Msg
}

func (r *Refusal) Error() string { return r.Msg.Render("en") }

// Localized is the refusal in lang (devtext.Text).
func (r *Refusal) Localized(lang string) string { return r.Msg.Render(lang) }

var (
	errBadName     = errors.New("bad name")     // i18n-ignore: internal, never shown (Join answers a Refusal)
	errBadPassword = errors.New("bad password") // i18n-ignore: internal, never shown (Join answers a Refusal)
)

func noControl(b []byte) bool {
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r < 0x20 || r == 0x7f {
			return false
		}
		b = b[size:]
	}
	return true
}

// validSSID checks a network name.
func validSSID(ssid string) error {
	if len(ssid) == 0 || len(ssid) > cMaxSSIDBytes || !utf8.ValidString(ssid) || !noControl([]byte(ssid)) {
		return errBadName
	}
	return nil
}

// validPSK checks a password: empty, 8-63 bytes of UTF-8 without control
// characters, or 64 hexadecimal digits.
func validPSK(psk []byte) error {
	switch n := len(psk); {
	case n == 0:
		return nil
	case n == cHexPSKBytes:
		for _, c := range psk {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return errBadPassword
			}
		}
		return nil
	case n >= cMinPSKBytes && n <= cMaxPSKBytes && utf8.Valid(psk) && noControl(psk):
		return nil
	}
	return errBadPassword
}

// Join asks the root runner to join ssid with password (empty for an open
// network; hidden: typed by hand, joined even when the scan doesn't show
// it), and answers the change as it starts ("joining"). It is refused
// (*Refusal) before anything is asked when the name or password break the
// rules, the device doesn't manage its Wi-Fi, an update was asked for or
// runs (updating, asked under the same lock UnlessChanging holds while it
// starts one), a change is already running, or too many were asked
// lately.
//
// The request carries the password in the clear, so it goes on the /run
// tmpfs only (the service's memory is locked too), owned by the otc user
// with mode 0600, and the bytes are zeroed once written. It is linked into
// place, never renamed over: a request already there is never replaced.
func Join(ssid string, password []byte, hidden bool, updating func() bool) (JoinStatus, error) {
	if validSSID(ssid) != nil {
		return JoinStatus{}, &Refusal{Code: "bad_name", Msg: i18n.DevDeviceWifiBadName()}
	}
	if validPSK(password) != nil {
		return JoinStatus{}, &Refusal{Code: "bad_password", Msg: i18n.DevDeviceWifiBadPassword()}
	}
	if !Managed() {
		return JoinStatus{}, &Refusal{Code: "not_managed", Msg: i18n.DevDeviceWifiNotManaged()}
	}

	mu.Lock()
	defer mu.Unlock()
	if updating != nil && updating() {
		return JoinStatus{}, &Refusal{Code: "update_running", Msg: i18n.DevStatusWifiJoinUpdateRunning()}
	}
	busy := &Refusal{Code: "busy", Msg: i18n.DevStatusWifiJoinBusy()}
	if current().inFlight {
		return JoinStatus{}, busy
	}
	final := filepath.Join(runDir, cRequestName)
	if _, err := os.Lstat(final); !errors.Is(err, os.ErrNotExist) {
		return JoinStatus{}, busy
	}
	t := now()
	kept := accepted[:0]
	for _, at := range accepted {
		if t.Sub(at) < cJoinWindow && !t.Before(at) {
			kept = append(kept, at)
		}
	}
	accepted = kept
	if len(accepted) >= cMaxJoins {
		return JoinStatus{}, &Refusal{Code: "too_many", Msg: i18n.DevStatusWifiJoinTooMany()}
	}

	var idRaw [8]byte
	if _, err := rand.Read(idRaw[:]); err != nil {
		return JoinStatus{}, err
	}
	id := hex.EncodeToString(idRaw[:])
	req := requestJSON(id, ssid, password, hidden)
	defer clear(req)
	if err := writeRequest(final, req); err != nil {
		if errors.Is(err, os.ErrExist) {
			return JoinStatus{}, busy
		}
		return JoinStatus{}, err
	}

	accepted = append(accepted, t)
	pending = &pendingJoin{id: id, ssid: ssid, at: t}
	log.Info("wifi: change requested")
	log.Debug("wifi: change requested to network", ssid)
	return JoinStatus{ID: id, SSID: ssid, State: StateJoining, Code: StateJoining}, nil
}

// requestJSON is the runner's request, {"v":1,"id":..,"ssid":..,"psk":..,
// "hidden":..}, built in one byte slice the caller zeroes (json.Marshal
// would leave copies of the password behind).
func requestJSON(id, ssid string, psk []byte, hidden bool) []byte {
	// Room for the worst case (every byte of both escaped) plus the 66
	// fixed bytes: append never reallocates, so no copy of the password is
	// left behind unzeroed.
	b := make([]byte, 0, 80+len(ssid)*2+len(psk)*2)
	b = append(b, `{"v":1,"id":"`...)
	b = append(b, id...)
	b = append(b, `","ssid":`...)
	b = appendJSONString(b, []byte(ssid))
	b = append(b, `,"psk":`...)
	b = appendJSONString(b, psk)
	if hidden {
		b = append(b, `,"hidden":true}`...)
	} else {
		b = append(b, `,"hidden":false}`...)
	}
	return b
}

// appendJSONString appends s as a JSON string. s is valid UTF-8 without
// control characters (validSSID, validPSK), so only '"' and '\' need
// escaping.
func appendJSONString(b, s []byte) []byte {
	b = append(b, '"')
	for _, c := range s {
		if c == '"' || c == '\\' {
			b = append(b, '\\')
		}
		b = append(b, c)
	}
	return append(b, '"')
}

// writeRequest writes req to a temporary file beside final (0600) and
// links it to final, which fails with os.ErrExist when a request is
// already there.
func writeRequest(final string, req []byte) error {
	f, err := os.CreateTemp(filepath.Dir(final), ".wifi-join-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(req); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp, final); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return err
	}
	return nil
}

// UnlessChanging runs start (an update starting) unless a Wi-Fi change is
// in flight, under the lock Join takes: an update and a change can't both
// pass each other's check. ran is false when a change was in flight.
func UnlessChanging(start func() error) (ran bool, err error) {
	mu.Lock()
	defer mu.Unlock()
	if current().inFlight {
		return false, nil
	}
	return true, start()
}
