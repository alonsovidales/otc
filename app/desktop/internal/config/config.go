// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config is the desktop sync client's persisted state: what the
// macOS app keeps in UserDefaults + the Keychain (SettingsStore.swift,
// SyncModel's bookmarks), as files under the user's config directory so
// the tray app, the command line and the Linux service all read and
// write the same thing.
//
//	config.json  - device address, the folders, autostart  (mode 0600)
//	secret       - the password, only when no keyring is available (0600)
//	state.json   - written by whichever process runs the sync engine, read
//	               by `otc-sync status` and by a tray that isn't the engine
//	lock         - held by the one process allowed to run the engine
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	appDirName     = "otc-sync"
	keyringService = "OffTheCloud"
	keyringUser    = "password"

	// BridgeDomain is where a device is reached by name alone (issue #121).
	BridgeDomain = "off-the.cloud"
)

// Folder is a local folder mirrored up to the device (SyncModel.TrackedFolder).
type Folder struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// RemoteFolder is a device directory kept in two-way sync with a local one
// (SyncModel.RemoteFolder, issue #47).
type RemoteFolder struct {
	ID         string `json:"id"`
	RemotePath string `json:"remote_path"`
	LocalPath  string `json:"local_path"`
}

// Config is config.json.
type Config struct {
	// Domain is a bare bridge host ("cala.off-the.cloud") or a full URL
	// ("ws://192.168.1.10:8080/ws") - exactly what WSClient.configure takes
	// on macOS.
	Domain        string         `json:"domain"`
	ClientID      string         `json:"client_id"`
	Folders       []Folder       `json:"folders"`
	RemoteFolders []RemoteFolder `json:"remote_folders"`
	// Autostart is whether the tray app registers itself to start at
	// login; on by default, like a menu bar sync client is expected to be.
	Autostart *bool `json:"autostart,omitempty"`
}

// Dir is the config directory, created on first use.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, appDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil { // perms: rwx------
		return "", err
	}

	return dir, nil
}

func path(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, name), nil
}

// Load reads config.json; a missing file is an empty config, not an error.
func Load() (*Config, error) {
	p, err := path("config.json")
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config.json is not valid: %w", err)
	}

	return cfg, nil
}

// Save writes config.json atomically, so a daemon watching it never reads
// a half-written file.
func (c *Config) Save() error {
	p, err := path("config.json")
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil { // perms: rw-------
		return err
	}

	return os.Rename(tmp, p)
}

// AutostartEnabled is the setting with its default applied.
func (c *Config) AutostartEnabled() bool {
	return c.Autostart == nil || *c.Autostart
}

// EnsureClientID gives this install a stable id for Auth.uuid, the way the
// mobile apps have a device id. Saved by the caller.
func (c *Config) EnsureClientID() bool {
	if c.ClientID != "" {
		return false
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	c.ClientID = hex.EncodeToString(b)

	return true
}

// NewID is an id for a folder entry.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// Ready mirrors SettingsStore.ready: both halves of the connection set.
func Ready(cfg *Config, password string) bool {
	return cfg.Domain != "" && password != ""
}

// ---- the password ------------------------------------------------------

// LoadPassword reads the password from the OS keyring (Windows Credential
// Manager, the Secret Service on a Linux desktop), or from the 0600 fallback
// file a headless install has to use - a service started at boot has no
// keyring to ask.
func LoadPassword() (string, error) {
	if pw, err := keyring.Get(keyringService, keyringUser); err == nil && !noKeyring() {
		return pw, nil
	}
	p, err := path("secret")
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(raw)), nil
}

// noKeyring is OTC_SYNC_NO_KEYRING=1: keep the password in the 0600 file
// even where a keyring exists - a headless box whose keyring only unlocks
// at login, or a developer's Mac where the keyring item would collide with
// the native app's own.
func noKeyring() bool { return os.Getenv("OTC_SYNC_NO_KEYRING") == "1" }

// SavePassword stores it in the keyring when there is one, else in the
// fallback file. Never logged.
func SavePassword(pw string) error {
	if pw == "" {
		_ = keyring.Delete(keyringService, keyringUser)
		p, err := path("secret")
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}

		return nil
	}
	if !noKeyring() {
		if err := keyring.Set(keyringService, keyringUser, pw); err == nil {
			// A stale fallback copy must not outlive the move to the keyring.
			if p, err := path("secret"); err == nil {
				_ = os.Remove(p)
			}

			return nil
		}
	}
	p, err := path("secret")
	if err != nil {
		return err
	}

	return os.WriteFile(p, []byte(pw+"\n"), 0o600) // perms: rw-------
}

// ---- the engine's status, for the CLI and a viewer tray ----------------

// FolderStatus is one folder as the engine last reported it.
type FolderStatus struct {
	ID          string  `json:"id"`
	Path        string  `json:"path"`
	RemotePath  string  `json:"remote_path,omitempty"`
	State       string  `json:"state"` // scanning | watching | error
	Progress    float64 `json:"progress"`
	CurrentFile string  `json:"current_file,omitempty"`
	Error       string  `json:"error,omitempty"`
}

// State is state.json.
type State struct {
	Status        string         `json:"status"` // Connected | Disconnected | Missing domain/password | Not connected
	Raid          string         `json:"raid"`   // ok | degraded | failed | unknown
	RaidSummary   string         `json:"raid_summary"`
	Folders       []FolderStatus `json:"folders"`
	RemoteFolders []FolderStatus `json:"remote_folders"`
	PID           int            `json:"pid"`
	Updated       time.Time      `json:"updated"`
}

// LoadState reads what the running engine last wrote; nil when none has.
func LoadState() (*State, error) {
	p, err := path("state.json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st := &State{}
	if err := json.Unmarshal(raw, st); err != nil {
		return nil, err
	}

	return st, nil
}

// SaveState is the engine's side of LoadState.
func SaveState(st *State) error {
	p, err := path("state.json")
	if err != nil {
		return err
	}
	st.PID = os.Getpid()
	st.Updated = time.Now()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil { // perms: rw-------
		return err
	}

	return os.Rename(tmp, p)
}

// LockPath is the file the engine holds a lock on while it runs.
func LockPath() (string, error) { return path("lock") }

// TrayLockPath is the file the one tray process per user holds a lock on.
func TrayLockPath() (string, error) { return path("tray.lock") }

// ConfigPath is where config.json lives, for the daemon to watch.
func ConfigPath() (string, error) { return path("config.json") }

// ---- the device address form (issue #121) ------------------------------

// BridgeDomainForName is "<name>.off-the.cloud".
func BridgeDomainForName(name string) string {
	return strings.ToLower(strings.TrimSpace(name)) + "." + BridgeDomain
}

// BridgeName is the device name when domain is a plain bridge host, else
// "" - which is how the settings form knows it is looking at a custom
// address. Mirrors SettingsStore.bridgeName(fromDomain:).
func BridgeName(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	if strings.HasPrefix(d, "wss://") {
		d = strings.TrimPrefix(d, "wss://")
		d = strings.TrimSuffix(d, "/ws")
		d = strings.Trim(d, "/")
	}
	if strings.Contains(d, "://") || strings.Contains(d, "/") || strings.Contains(d, ":") || !strings.HasSuffix(d, "."+BridgeDomain) {
		return ""
	}
	name := strings.TrimSuffix(d, "."+BridgeDomain)
	if name == "" || strings.Contains(name, ".") {
		return ""
	}

	return name
}
