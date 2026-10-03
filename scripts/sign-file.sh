#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# sign-file.sh FILE SIGFILE - writes the base64 Ed25519 signature of FILE,
# made with the project's release key (~/.otc/otc-release-signing.pem, its
# passphrase in the Keychain item otc-release-signing; see release.sh), and
# checks it with the public key devices and apps pin. The key is decrypted
# with `openssl pkcs8` straight into the signer, never written; retried,
# since signing has been seen to fail now and then and work a moment later.
set -euo pipefail
FILE="${1:?usage: sign-file.sh FILE SIGFILE}"; OUT="${2:?usage: sign-file.sh FILE SIGFILE}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KEY="${OTC_RELEASE_KEY:-$HOME/.otc/otc-release-signing.pem}"
OPENSSL="$(command -v /opt/homebrew/bin/openssl || command -v openssl)"
KEY_PASS="$(security find-generic-password -s otc-release-signing -a otc -w 2>/dev/null)" \
    || { echo "can't read the signing key's passphrase from the Keychain (locked? run: security unlock-keychain)" >&2; exit 1; }
export KEY_PASS
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
for attempt in 1 2 3 4 5; do
    if "$OPENSSL" pkcs8 -in "$KEY" -passin env:KEY_PASS 2>/dev/null \
        | "$OPENSSL" pkeyutl -sign -inkey /dev/stdin -rawin -in "$FILE" -out "$tmp/sig" 2>/dev/null; then
        "$OPENSSL" pkeyutl -verify -pubin -inkey "$ROOT/scripts/release-signing.pub" -rawin -in "$FILE" -sigfile "$tmp/sig" >/dev/null
        base64 < "$tmp/sig" | tr -d '\n' > "$OUT"; echo >> "$OUT"
        exit 0
    fi
    sleep 2
done
echo "signing $FILE failed" >&2; exit 1
