// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
)

// Policy is the pages' Content-Security-Policy: their inline style and
// scripts by nonce, and nothing from anywhere but the bridge itself (the
// self-hosted fonts and their stylesheet, the logo and screenshots, the
// contact form's POST, the account page's calls to /api/account), never
// in a frame. The JSON blocks holding the scripts' text are data, never
// run. Every page the bridge renders goes out under it: package api's
// (writeSitePage) and the sign-in's own (bridge/accounts' writePage).
func Policy(nonce string) string {
	return strings.ReplaceAll(cPolicy, "NONCE", "'nonce-"+nonce+"'")
}

const cPolicy = "default-src 'none'; script-src NONCE; style-src 'self' NONCE; font-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'" // i18n-ignore: a Content-Security-Policy header, not text

// NewNonce returns 128 random bits for one response's policy (Options.Nonce).
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
