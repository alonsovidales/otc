// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"os"
	"testing"
	"time"
)

// TestMain runs the package with an offline countdown longer than any
// run. A device connection a test drops starts the real one
// (cOfflineAlertGrace, 60 s), and most tests' managers have no database
// for its alert: on a slow machine the package outlasted the countdown
// and the alert dereferenced the missing database. The tests about the
// alert set a short countdown of their own.
func TestMain(m *testing.M) {
	cOfflineAlertGrace = 24 * time.Hour
	os.Exit(m.Run())
}
