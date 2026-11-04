// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMain points the home, config and data folders (and with them the
// trash, trash.go) at a scratch folder for the whole run, so a test that
// doesn't call withConfigDir still never writes into the real ones; on
// Windows the Recycle Bin is left alone.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "otc-sync-engine-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"AppData":         home,
	} {
		_ = os.Setenv(k, v)
	}
	if runtime.GOOS == "windows" {
		// The Recycle Bin is the user's own: deletes go to the folder's.
		trashSystem = func(string) error { return errors.New("not in the tests") }
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
