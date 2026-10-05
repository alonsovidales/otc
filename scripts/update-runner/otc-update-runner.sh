#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The root half of the Update button (issue #94).
#
# otc.service runs as the unprivileged otc user with NoNewPrivileges=true,
# so nothing the device shells out to can ever become root - sudo fails
# there with "the no new privileges flag is set" whatever sudoers says. An
# update has to run as root (it installs the binary and restarts the
# service), so the device doesn't run it: it drops a trigger file, and
# systemd's otc-update.path starts this script as root in a unit of its
# own (otc-update.service). Same shape as the Tailscale operator in
# release 2 - the device never holds a path to root.
#
# Trust boundary: the trigger file is only a trigger. Where the update is
# fetched from comes from the root-owned config the service runs with
# ([otc] update-repo / update-releases, defaulting to this project's own
# repository), never from anything the otc user can write. The runner
# itself is downloaded here, into a root-only directory, and run from
# there - not from a file the device could have altered.
set -uo pipefail

REQUEST=/var/lib/otc/update.request
RUN_DIR=/run/otc-update
STATUS_FILE=/var/lib/otc/update-status.json
DEFAULT_RAW=https://raw.githubusercontent.com/alonsovidales/otc/main
DEFAULT_GH=https://github.com/alonsovidales/otc

# Consumed first: a press that lands while this run is busy leaves a fresh
# file behind, and the path unit picks that one up once this run is done.
rm -f "$REQUEST"

# Written to a fresh temp file and moved into place: the directory is the
# otc user's, and a plain "> $STATUS_FILE" as root followed a symlink it
# could have put there - to overwrite (or chmod) any file on the system.
# dd's conv=excl creates the temp file with O_CREAT|O_EXCL, which refuses
# any name already there (a planted symlink included), and umask 022 gives
# it its final mode, so there is no chmod for a symlink swapped in later
# to redirect. mv replaces a symlink rather than following it.
status() {
    local tmp="$STATUS_FILE.tmp.$$.$RANDOM$RANDOM"
    printf '{"state":%s,"message":%s,"version":%s,"updated":%s}\n' \
        "\"$1\"" "\"${2//\"/\\\"}\"" "\"$(cat /etc/otc/version 2>/dev/null || echo unknown)\"" \
        "\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"" \
        | (umask 022; dd of="$tmp" conv=excl status=none 2>/dev/null) || { rm -f "$tmp"; return 1; }
    mv -Tf "$tmp" "$STATUS_FILE"
}

# The service's own config decides the repository, the same way the
# device's Check does (see updater.go's repoRaw/repoGH).
ini_value() {
    local ini="$1" key="$2"
    awk -F= -v key="$key" '
        /^[[:space:]]*\[/ { section = $0; gsub(/[[:space:]]/, "", section) }
        section == "[otc]" {
            k = $1; gsub(/[[:space:]]/, "", k)
            if (k == key) { v = $2; sub(/^[[:space:]]+/, "", v); sub(/[[:space:]]+$/, "", v); print v; exit }
        }' "$ini" 2>/dev/null
}
env_name="$(systemctl show -p ExecStart --value otc.service 2>/dev/null \
    | sed -n 's/.*argv\[\]=\/usr\/bin\/otc \([^ ;]*\).*/\1/p' | head -1)"
ini="/etc/otc_${env_name:-dev}.ini"
raw="$(ini_value "$ini" update-repo)"
gh="$(ini_value "$ini" update-releases)"
export OTC_REPO_RAW="${raw:-$DEFAULT_RAW}"
export OTC_REPO_GH="${gh:-$DEFAULT_GH}"
OTC_REPO_RAW="${OTC_REPO_RAW%/}"
OTC_REPO_GH="${OTC_REPO_GH%/}"
case "$OTC_REPO_RAW" in https://*) ;; *) status failed "refusing a non-HTTPS update repository: $OTC_REPO_RAW"; exit 1 ;; esac

# Issue #160: nothing from the repository is run unverified. The release
# manifest is signed with the project's release key (Ed25519, kept off
# GitHub; scripts/release.sh) and checked here against the public key this
# device pins in a root-owned file; the manifest carries the hash of each
# release's own source archive, and the updater that runs is the one in
# that verified archive - never a file fetched from a branch.
KEY=/etc/otc/release-signing.pub
[ -f "$KEY" ] || { status failed "this device has no release signing key ($KEY) - reinstall or update by hand"; exit 1; }

status running "Checking the release signature"
rm -rf "$RUN_DIR" && mkdir -p "$RUN_DIR" && chmod 700 "$RUN_DIR"
# Connect and stall timeouts: a transfer that stopped mid-way otherwise
# hangs the unit for good. Under 1 byte/s for two minutes has truly
# stopped (exit 28, retried); a slow but live link is never cut off.
fetch() { curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 30 --speed-limit 1 --speed-time 120 -o "$2" "$1"; }
fetch "$OTC_REPO_RAW/scripts/updates/VERSIONS" "$RUN_DIR/VERSIONS" \
    && fetch "$OTC_REPO_RAW/scripts/updates/VERSIONS.sig" "$RUN_DIR/VERSIONS.sig.b64" \
    || { status failed "could not download the release manifest"; exit 1; }
base64 -d < "$RUN_DIR/VERSIONS.sig.b64" > "$RUN_DIR/VERSIONS.sig" 2>/dev/null \
    && openssl pkeyutl -verify -pubin -inkey "$KEY" -rawin -in "$RUN_DIR/VERSIONS" -sigfile "$RUN_DIR/VERSIONS.sig" >/dev/null 2>&1 \
    || { status failed "the release manifest is not signed with this project's release key - nothing was installed"; exit 1; }

installed="$(cat /etc/otc/version 2>/dev/null || echo 0)"
target=""; src_sha=""
while IFS=$'\t' read -r version _script _assets _summary src; do
    case "$version" in ''|\#*) continue ;; esac
    if [ "$version" -gt "$installed" ] 2>/dev/null; then target="$version"; src_sha="$src"; fi
done < "$RUN_DIR/VERSIONS"
if [ -z "$target" ]; then
    status uptodate "Already up to date"
    exit 0
fi
case "$src_sha" in [0-9a-f]*) ;; *) status failed "release $target has no signed source archive"; exit 1 ;; esac

status running "Downloading release $target"
fetch "$OTC_REPO_GH/releases/download/v$target/src.tar.gz" "$RUN_DIR/src.tar.gz" \
    || { status failed "could not download the source of release $target"; exit 1; }
actual="$(sha256sum "$RUN_DIR/src.tar.gz" | awk '{print $1}')"
[ "$actual" = "$src_sha" ] || { status failed "the source of release $target does not match its signed hash"; exit 1; }
mkdir -p "$RUN_DIR/src" && tar -xzf "$RUN_DIR/src.tar.gz" -C "$RUN_DIR/src" --strip-components=1 \
    || { status failed "could not unpack release $target"; exit 1; }

export OTC_VERIFIED_MANIFEST="$RUN_DIR/VERSIONS" OTC_VERIFIED_SRC="$RUN_DIR/src"
exec /bin/bash "$RUN_DIR/src/scripts/update.sh"
