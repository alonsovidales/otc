#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Installs the newest SIGNED release of Off The Cloud (issue #160) - what
# the setup wizard runs, and the one-liner for installing by hand:
#
#   curl -fsSL https://raw.githubusercontent.com/alonsovidales/otc/main/scripts/verified-install.sh | sudo bash -s -- <subdomain>
#
# Nothing from the repository's branch is run: the release manifest
# (scripts/updates/VERSIONS) must carry a signature by the project's release
# key (Ed25519, kept off GitHub), and the release's own source archive and
# web bundle must match the hashes it lists. Only then does the install.sh
# inside that verified source run, with OTC_VERIFIED_SRC, OTC_VERIFIED_WEB
# and OTC_RELEASE_VERSION set. The key is /etc/otc/release-signing.pub when
# present (the image ships it), else the copy below - so a hand install
# trusts this one small script once, and nothing else.
set -euo pipefail

REPO_RAW="${OTC_REPO_RAW:-https://raw.githubusercontent.com/alonsovidales/otc/main}"
REPO_GH="${OTC_REPO_GH:-https://github.com/alonsovidales/otc}"
STAGE=/opt/otc-verified
RELEASE_KEY='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAtVgLIKBzcqMNM2nUnK9xfgpqWrLTuZsk8ylhyI0BK9g=
-----END PUBLIC KEY-----'

die() { echo "[otc-install] ERROR: $*" >&2; exit 1; }
[ "$(id -u)" -eq 0 ] || die "needs root - re-run with sudo"
command -v openssl >/dev/null || die "openssl is missing"

rm -rf "$STAGE" && mkdir -p "$STAGE" && chmod 700 "$STAGE"
if [ -f /etc/otc/release-signing.pub ]; then
    cp /etc/otc/release-signing.pub "$STAGE/key.pub"
else
    printf '%s\n' "$RELEASE_KEY" > "$STAGE/key.pub"
fi
# Connect and stall timeouts, so a transfer that stopped mid-way fails
# (and is retried) instead of hanging the setup for good.
fetch() { curl -fsSL --retry 5 --retry-delay 2 --connect-timeout 30 --speed-limit 1 --speed-time 120 -o "$2" "$1"; }

echo "[otc-install] Checking the release signature"
fetch "$REPO_RAW/scripts/updates/VERSIONS" "$STAGE/VERSIONS" || die "could not download the release manifest"
fetch "$REPO_RAW/scripts/updates/VERSIONS.sig" "$STAGE/VERSIONS.sig.b64" || die "could not download the manifest's signature"
base64 -d < "$STAGE/VERSIONS.sig.b64" > "$STAGE/VERSIONS.sig" 2>/dev/null \
    && openssl pkeyutl -verify -pubin -inkey "$STAGE/key.pub" -rawin -in "$STAGE/VERSIONS" -sigfile "$STAGE/VERSIONS.sig" >/dev/null 2>&1 \
    || die "the release manifest is not signed with the project's release key - nothing was installed"

version=""; web_sha=""; src_sha=""
while IFS=$'\t' read -r v _script web _summary src; do
    case "$v" in ''|\#*) continue ;; esac
    version="$v"; web_sha="$web"; src_sha="$src"
done < "$STAGE/VERSIONS"
case "$src_sha" in [0-9a-f]*) ;; *) die "release $version has no signed source archive" ;; esac
check() { [ "$(sha256sum "$1" | awk '{print $1}')" = "$2" ] || die "$3 does not match its signed hash"; }

echo "[otc-install] Downloading release $version"
fetch "$REPO_GH/releases/download/v$version/src.tar.gz" "$STAGE/src.tar.gz" || die "could not download release $version"
check "$STAGE/src.tar.gz" "$src_sha" "the source of release $version"
mkdir -p "$STAGE/src" && tar -xzf "$STAGE/src.tar.gz" -C "$STAGE/src" --strip-components=1
case "$web_sha" in
    [0-9a-f]*)
        fetch "$REPO_GH/releases/download/v$version/web-dist.tar.gz" "$STAGE/web-dist.tar.gz" || die "could not download the web app of release $version"
        check "$STAGE/web-dist.tar.gz" "$web_sha" "the web app of release $version" ;;
    *) die "release $version has no signed web app" ;;
esac
echo "[otc-install] Release $version verified (signature, source and web app)"

export OTC_VERIFIED_SRC="$STAGE/src" OTC_VERIFIED_WEB="$STAGE/web-dist.tar.gz" OTC_RELEASE_VERSION="$version"
exec bash "$STAGE/src/scripts/install.sh" "$@"
