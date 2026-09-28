// SPDX-License-Identifier: AGPL-3.0-or-later

// Package bridgeaccess switches the bridge on for a device that was set up
// without it (issue #145) - what the setup wizard does when an account is
// chosen, done later from Settings.
//
// Two halves, split by privilege. This one runs as the service (the otc
// user): it signs the owner in to an Off The Cloud account, reserves the
// name on the bridge with a fresh identity and stores that identity in the
// database, all things the device can do itself. The other half needs
// root - the bridge is switched on by the root-owned config file, and the
// service has to restart to use it - so, like the Update button, the
// device only drops a trigger file and systemd's otc-bridge.path runs
// scripts/bridge-runner/otc-bridge-runner.sh as root. The trigger carries
// nothing: which bridge to join comes from the root-owned config, never
// from a file this user can write.
package bridgeaccess

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/google/uuid"
)

const (
	// cDefaultBridge is the bridge a device joins when its config names
	// none ([otc] bridge-default, written by install.sh).
	cDefaultBridge = "off-the.cloud"

	cRequestPath = "/var/lib/otc/bridge.request"
	cStatusFile  = "/var/lib/otc/bridge-status.json"
	cRootRunner  = "/usr/local/bin/otc-bridge-runner"

	cTimeout = 15 * time.Second
)

// nameRE is the bridge's own rule for a name (the setup wizard's NAME_RE).
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var client = &http.Client{Timeout: cTimeout}

// Enabled reports whether the device is on a bridge.
func Enabled() bool {
	return cfg.HasSection("otc") && cfg.GetStr("otc", "bridge-addr") != ""
}

// Bridge is the bridge this device is on, or would join.
func Bridge() string {
	if cfg.HasSection("otc") {
		if b := strings.TrimSpace(cfg.GetStr("otc", "bridge-addr")); b != "" {
			return b
		}
		if b := strings.TrimSpace(cfg.GetStr("otc", "bridge-default")); b != "" {
			return b
		}
	}

	return cDefaultBridge
}

// Status is where a switch-on stands: pending while the root side hasn't
// finished it (the device restarts at the end, so a finished one is simply
// Enabled), and the root side's error if it gave up.
func Status() (pending bool, failure string) {
	if _, err := os.Stat(cRequestPath); err == nil {
		return true, ""
	}
	raw, err := os.ReadFile(cStatusFile)
	if err != nil {
		return false, ""
	}
	var st struct{ State, Message string }
	if json.Unmarshal(raw, &st) != nil {
		return false, ""
	}
	switch st.State {
	case "running":
		return true, ""
	case "failed":
		return false, st.Message
	}

	return false, ""
}

// Providers are the account sign-ins the bridge offers besides email.
func Providers() []string {
	var out struct {
		Providers []string `json:"providers"`
	}
	if status, err := call(http.MethodGet, "/api/account/providers", nil, &out); err != nil || status != http.StatusOK {
		return nil
	}
	var known []string
	for _, p := range out.Providers {
		if p == "apple" || p == "google" {
			known = append(known, p)
		}
	}

	return known
}

// SignIn signs in to an Off The Cloud account for setting a device up and
// returns the setup token that proves it, and the account's email.
func SignIn(email, password string) (token, accountEmail string, err error) {
	var out struct {
		SetupToken string `json:"setup_token"`
		Error      string `json:"error"`
		Account    struct {
			Email string `json:"email"`
		} `json:"account"`
	}
	status, err := call(http.MethodPost, "/api/account/login?for=setup",
		map[string]string{"email": email, "password": password}, &out)
	if err != nil {
		return "", "", err
	}
	if status != http.StatusOK || out.SetupToken == "" {
		return "", "", errors.New(orDefault(out.Error, "could not sign in"))
	}

	return out.SetupToken, orDefault(out.Account.Email, email), nil
}

// CodeOwner checks a setup code - typed, or handed back by a provider
// sign-in - and returns the email of the account it belongs to.
func CodeOwner(code string) (string, error) {
	var out struct {
		Email string `json:"email"`
	}
	status, err := call(http.MethodGet, "/api/account/setup-token-info?token="+url.QueryEscape(code), nil, &out)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errors.New("that setup code is not valid or has expired")
	}

	return out.Email, nil
}

// Identity is what the device registers with on the bridge.
type Identity struct {
	Domain     string
	DeviceUuid string
	Secret     string
}

// Claim reserves name on the bridge for the account behind setupToken,
// with a fresh identity - exactly the setup wizard's claim. A name the
// account already holds is handed to this device.
func Claim(name, setupToken string) (Identity, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !nameRE.MatchString(name) {
		return Identity{}, errors.New("lower-case letters, digits and hyphens only")
	}
	if setupToken == "" {
		return Identity{}, errors.New("sign in to your Off The Cloud account first")
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return Identity{}, err
	}
	id := Identity{DeviceUuid: uuid.New().String(), Secret: hex.EncodeToString(secret)}
	var out struct {
		Domain string `json:"domain"`
		Error  string `json:"error"`
	}
	status, err := call(http.MethodPost, "/api/claim", map[string]string{
		"name": name, "owner_uuid": id.DeviceUuid, "secret": id.Secret, "setup_token": setupToken,
	}, &out)
	if err != nil {
		return Identity{}, err
	}
	if status != http.StatusCreated {
		return Identity{}, errors.New(orDefault(out.Error, "could not reserve that name"))
	}
	id.Domain = orDefault(out.Domain, name+"."+Bridge())

	return id, nil
}

// CanSwitchOn reports whether this device has the root side installed -
// one set up before this feature gets it with its next update.
func CanSwitchOn() error {
	if _, err := os.Stat(cRootRunner); err != nil {
		return errors.New("this device needs updating before it can join the bridge: Settings → Update")
	}

	return nil
}

// RequestSwitchOn hands the rest to the root side, which turns the bridge
// on in the config and restarts the service.
func RequestSwitchOn() error {
	if err := CanSwitchOn(); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(cRequestPath, []byte(stamp), 0o644); err != nil { // perms: rw-r--r--
		return fmt.Errorf("switching the bridge on: %w", err)
	}

	return nil
}

func call(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "https://"+Bridge()+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("could not reach %s: %w", Bridge(), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(raw, out)

	return resp.StatusCode, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}

	return v
}
