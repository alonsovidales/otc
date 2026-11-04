// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestMain keeps the system's keyring out of every test (see the config
// package's).
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}
