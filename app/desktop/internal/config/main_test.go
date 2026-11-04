// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestMain keeps the system's keyring out of every test: a password saved,
// read or forgotten goes to an in-memory one (the Mac app's item shares
// the keyring's name on a developer's Mac).
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}
