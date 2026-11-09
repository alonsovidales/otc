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
# gh_api_fetch <url> <out>: a github.com release file fetched through
# GitHub's API instead of its download link. On 2026-11-09 those links
# answered 504 for over ten minutes, for some files and not others, while
# the API served the very same files. Only for a
# https://github.com/<owner>/<repo>/releases/download/<tag>/<name> link;
# what it fetches is used only once its hash matches (the signed manifest's,
# or a pin), as from the link. The same function is in otc-update-runner.sh,
# update.sh, verified-install.sh and install.sh (updater/gh_fetch_test.go).
gh_api_fetch() {
    local url="$1" out="$2" repo="" tag="" name="" id=""
    local re='^https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/releases/download/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$'
    local stall=(--connect-timeout 30 --speed-limit 1 --speed-time 120)
    [[ "$url" =~ $re ]] || return 1
    repo="${BASH_REMATCH[1]}" tag="${BASH_REMATCH[2]}" name="${BASH_REMATCH[3]}"
    echo "GitHub's download link for $name failed: fetching it through GitHub's API" >&2
    # The release into a file, never a pipe: curl starts a retried
    # transfer's file afresh, while what it had sent down a pipe would stay
    # ahead of the retry's answer.
    if curl -fsSL --retry 3 --max-time 60 --max-filesize 1048576 "${stall[@]}" \
            -H 'Accept: application/vnd.github+json' -o "$out.release" \
            "https://api.github.com/repos/$repo/releases/tags/$tag"; then
        id="$(python3 -I -c 'import json, sys
for a in json.load(sys.stdin).get("assets") or []:
    if a.get("name") == sys.argv[1] and type(a.get("id")) is int:
        print(a["id"])
        break' "$name" < "$out.release" 2>/dev/null)" || true
    fi
    rm -f -- "$out.release"
    [[ "$id" =~ ^[0123456789]{1,20}$ ]] || return 1
    curl -fsSL --retry 6 --retry-all-errors "${stall[@]}" -H 'Accept: application/octet-stream' \
        -o "$out" "https://api.github.com/repos/$repo/releases/assets/$id"
}
# gh_fetch <url> <out>: a release's file, retried for about a minute (1 s
# doubling), then through GitHub's API (gh_api_fetch above).
gh_fetch() {
    curl -fsSL --retry 6 --retry-all-errors --connect-timeout 30 --speed-limit 1 --speed-time 120 -o "$2" "$1" \
        || gh_api_fetch "$1" "$2"
}

echo "[otc-install] Checking the release signature"
# Right after a release the CDN can serve the new manifest beside the old
# signature for a while: a signature that doesn't match is fetched again
# for about a minute before the install gives up.
for try in 1 2 3 4 5 6 7; do
    fetch "$REPO_RAW/scripts/updates/VERSIONS" "$STAGE/VERSIONS" || die "could not download the release manifest"
    fetch "$REPO_RAW/scripts/updates/VERSIONS.sig" "$STAGE/VERSIONS.sig.b64" || die "could not download the manifest's signature"
    if base64 -d < "$STAGE/VERSIONS.sig.b64" > "$STAGE/VERSIONS.sig" 2>/dev/null \
        && openssl pkeyutl -verify -pubin -inkey "$STAGE/key.pub" -rawin -in "$STAGE/VERSIONS" -sigfile "$STAGE/VERSIONS.sig" >/dev/null 2>&1; then
        break
    fi
    [ "$try" -lt 7 ] || die "could not verify the newest release yet - nothing was installed, try again in a few minutes"
    echo "[otc-install] The release manifest's signature doesn't match (yet): fetching it again in 10 s"
    sleep 10
done

version=""; web_sha=""; src_sha=""
while IFS=$'\t' read -r v _script web _summary src; do
    case "$v" in ''|\#*) continue ;; esac
    version="$v"; web_sha="$web"; src_sha="$src"
done < "$STAGE/VERSIONS"
case "$src_sha" in [0-9a-f]*) ;; *) die "release $version has no signed source archive" ;; esac
check() { [ "$(sha256sum "$1" | awk '{print $1}')" = "$2" ] || die "$3 does not match its signed hash"; }

echo "[otc-install] Downloading release $version"
gh_fetch "$REPO_GH/releases/download/v$version/src.tar.gz" "$STAGE/src.tar.gz" || die "could not download release $version"
check "$STAGE/src.tar.gz" "$src_sha" "the source of release $version"
mkdir -p "$STAGE/src" && tar -xzf "$STAGE/src.tar.gz" -C "$STAGE/src" --strip-components=1
case "$web_sha" in
    [0-9a-f]*)
        gh_fetch "$REPO_GH/releases/download/v$version/web-dist.tar.gz" "$STAGE/web-dist.tar.gz" || die "could not download the web app of release $version"
        check "$STAGE/web-dist.tar.gz" "$web_sha" "the web app of release $version" ;;
    *) die "release $version has no signed web app" ;;
esac
echo "[otc-install] Release $version verified (signature, source and web app)"

export OTC_VERIFIED_SRC="$STAGE/src" OTC_VERIFIED_WEB="$STAGE/web-dist.tar.gz" OTC_RELEASE_VERSION="$version"
exec bash "$STAGE/src/scripts/install.sh" "$@"
