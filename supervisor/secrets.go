// SPDX-License-Identifier: AGPL-3.0-or-later

package supervisor

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateSecret returns a random hex string nBytes long before encoding -
// same shape as `openssl rand -hex N` already used elsewhere in this
// project's own bootstrap tooling (scripts/install.sh's DEVICE_UUID/
// BRIDGE_SECRET/OTC_DB_PASS generation), just from Go instead of a shell
// one-liner, since this runs at request-handling time inside the running
// primary process (ReqCreateUser), not install-time.
func GenerateSecret(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
