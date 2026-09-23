// SPDX-License-Identifier: AGPL-3.0-or-later

package wsclient

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
)

// EncryptPassword is PwCrypto.swift: the bridge relays app payloads
// between a client and a device, so the password is encrypted end to end
// with the ephemeral RSA public key the device hands out per connection
// (GetPubKey/PubKey, issue #2). RSA-OAEP with SHA-256, the key being the
// X.509 SubjectPublicKeyInfo DER that Go's x509.MarshalPKIXPublicKey
// produces on the device.
func EncryptPassword(password string, pubKeyDER []byte) ([]byte, error) {
	parsed, err := x509.ParsePKIXPublicKey(pubKeyDER)
	if err != nil {
		return nil, fmt.Errorf("malformed public key: %w", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("the connection's public key is not RSA")
	}

	return rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, []byte(password), nil)
}
