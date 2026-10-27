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
VERSION_FILE=/etc/otc/version
MIGRATED_FILE=/etc/otc/version.migrated
DEFAULT_RAW=https://raw.githubusercontent.com/alonsovidales/otc/main
DEFAULT_GH=https://github.com/alonsovidales/otc

# Consumed first: a press that lands while this run is busy leaves a fresh
# file behind, and the path unit picks that one up once this run is done.
rm -f "$REQUEST"

# read_number <file>: the release number in a version file, or nothing
# (status 1) when it is absent, empty or not a number - a write cut short
# by a power cut leaves such a file.
read_number() {
    local v
    v="$(head -c 32 "$1" 2>/dev/null | head -n 1 | tr -d '[:space:]')"
    [[ "$v" =~ ^[0-9]{1,9}$ ]] || return 1
    printf '%s' "$((10#$v))"
}

# Written to a fresh temp file and moved into place: the directory is the
# otc user's, and a plain "> $STATUS_FILE" as root followed a symlink it
# could have put there - to overwrite (or chmod) any file on the system.
# dd's conv=excl creates the temp file with O_CREAT|O_EXCL, which refuses
# any name already there (a planted symlink included), and umask 022 gives
# it its final mode, so there is no chmod for a symlink swapped in later
# to redirect. mv replaces a symlink rather than following it.
#
# status <state> <message> [<code> [<args>]]: message is the English that
# devices from before codes show; code names the same text in the
# device's catalog (dev.update.<code>, devtext's table) and args, from
# args() below, its arguments - so a newer device shows it in its owner's
# language (docs/i18n.md, "Text written before any request").
status() {
    local tmp="$STATUS_FILE.tmp.$$.$RANDOM$RANDOM" code="${3:-}" code_args="${4:-}"
    [ -n "$code_args" ] || code_args='{}'
    printf '{"state":%s,"message":%s,"code":%s,"args":%s,"version":%s,"updated":%s}\n' \
        "\"$1\"" "\"${2//\"/\\\"}\"" "\"$code\"" "$code_args" "\"$(read_number "$VERSION_FILE" || echo unknown)\"" \
        "\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"" \
        | (umask 022; dd of="$tmp" conv=excl status=none 2>/dev/null) || { rm -f "$tmp"; return 1; }
    mv -Tf "$tmp" "$STATUS_FILE"
}

# args <name> <value>...: a status code's arguments as a JSON object of
# strings. Backslashes and quotes are escaped and control characters
# dropped, so no value (a configured URL, say) can leave its string.
args() {
    local out="" v
    while [ "$#" -ge 2 ]; do
        v="${2//\\/\\\\}"
        v="${v//\"/\\\"}"
        v="$(printf '%s' "$v" | tr -d '\000-\037')"
        out="${out:+$out,}\"$1\":\"$v\""
        shift 2
    done
    printf '{%s}' "$out"
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
case "$OTC_REPO_RAW" in https://*) ;; *) status failed "refusing a non-HTTPS update repository: $OTC_REPO_RAW" repo_not_https "$(args url "$OTC_REPO_RAW")"; exit 1 ;; esac

# Issue #160: nothing from the repository is run unverified. The release
# manifest is signed with the project's release key (Ed25519, kept off
# GitHub; scripts/release.sh) and checked here against the public key this
# device pins in a root-owned file; the manifest carries the hash of each
# release's own source archive, and the updater that runs is the one in
# that verified archive - never a file fetched from a branch.
KEY=/etc/otc/release-signing.pub
[ -f "$KEY" ] || { status failed "this device has no release signing key ($KEY) - reinstall or update by hand" no_release_key "$(args path "$KEY")"; exit 1; }

status running "Checking the release signature" checking_signature
rm -rf "$RUN_DIR" && mkdir -p "$RUN_DIR" && chmod 700 "$RUN_DIR"
# /run is tmpfs (RAM): nothing staged here outlives the run. exec drops
# this trap, and update.sh then cleans up after itself (cleanup_stage).
trap 'rm -rf "$RUN_DIR"' EXIT
# Connect and stall timeouts: a transfer that stopped mid-way otherwise
# hangs the unit for good. Under 1 byte/s for two minutes has truly
# stopped (exit 28, retried); a slow but live link is never cut off.
fetch() { curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 30 --speed-limit 1 --speed-time 120 -o "$2" "$1"; }
# Right after a release the CDN can serve the new manifest beside the old
# signature (or the other way round) for a while, which reads exactly like
# a manifest someone altered: a signature that doesn't match is fetched
# again for about a minute before the update gives up - saying so in words
# that don't sound like an attack, since it nearly never is one. Nothing
# unverified is ever used either way.
for try in 1 2 3 4 5 6 7; do
    fetch "$OTC_REPO_RAW/scripts/updates/VERSIONS" "$RUN_DIR/VERSIONS" \
        && fetch "$OTC_REPO_RAW/scripts/updates/VERSIONS.sig" "$RUN_DIR/VERSIONS.sig.b64" \
        || { status failed "could not download the release manifest" manifest_download_failed; exit 1; }
    base64 -d < "$RUN_DIR/VERSIONS.sig.b64" > "$RUN_DIR/VERSIONS.sig" 2>/dev/null \
        && openssl pkeyutl -verify -pubin -inkey "$KEY" -rawin -in "$RUN_DIR/VERSIONS" -sigfile "$RUN_DIR/VERSIONS.sig" >/dev/null 2>&1 \
        && break
    [ "$try" -lt 7 ] || { status failed "could not verify this release yet - nothing was installed, try again in a few minutes" manifest_not_signed; exit 1; }
    sleep 10
done

# No version file is a device from before updates existed: release 0, the
# whole history. One that can't be read (a write cut short) used to read
# as "up to date" for good: its version comes back from version.migrated,
# which every update writes before it, and is written again; with neither
# readable the update stops and says so (update.sh does the same).
if [ ! -e "$VERSION_FILE" ]; then
    installed=0
elif ! installed="$(read_number "$VERSION_FILE")"; then
    if installed="$(read_number "$MIGRATED_FILE")"; then
        { printf '%s\n' "$installed" > "$VERSION_FILE.tmp.$$" && { sync "$VERSION_FILE.tmp.$$" 2>/dev/null || sync; } \
            && mv -Tf "$VERSION_FILE.tmp.$$" "$VERSION_FILE"; } || rm -f "$VERSION_FILE.tmp.$$"
    else
        status failed "this device's version file ($VERSION_FILE) can't be read - reinstall or update by hand" version_unreadable "$(args path "$VERSION_FILE")"; exit 1
    fi
fi
target=""; src_sha=""
while IFS=$'\t' read -r version _script _assets _summary src; do
    case "$version" in ''|\#*) continue ;; esac
    if [ "$version" -gt "$installed" ] 2>/dev/null; then target="$version"; src_sha="$src"; fi
done < "$RUN_DIR/VERSIONS"
if [ -z "$target" ]; then
    status uptodate "Already up to date" uptodate
    exit 0
fi
case "$src_sha" in [0-9a-f]*) ;; *) status failed "release $target has no signed source archive" no_source_archive "$(args release "$target")"; exit 1 ;; esac

# Before anything is downloaded: the release's source archive (about 22
# MB) and the source unpacked from it (about 50 MB) are staged here in RAM,
# and the build that follows takes up to about 1 GB of the system disk
# (update.sh checks that again before it starts). Failing now says why;
# running out half-way said nothing useful.
for space in "$RUN_DIR 150" "/ 1024"; do
    path="${space% *}"; need="${space#* }"
    kb="$(df -Pk "$path" 2>/dev/null | awk 'NR == 2 {print $4}')"
    { [[ "$kb" =~ ^[0-9]+$ ]] && [ "$kb" -lt $((need * 1024)) ]; } || continue
    free_h="$(awk -v k="$kb" 'BEGIN { if (k >= 1048576) printf "%.1f GB", k / 1048576; else printf "%d MB", k / 1024 }')"
    need_h="$(awk -v m="$need" 'BEGIN { if (m >= 1024) printf "%.1f GB", m / 1024; else printf "%d MB", m }')"
    status failed "not enough free space for the update: $path has $free_h free, it needs $need_h" no_space "$(args path "$path" free "$free_h" needed "$need_h")"; exit 1
done

status running "Downloading release $target" downloading "$(args release "$target")"
fetch "$OTC_REPO_GH/releases/download/v$target/src.tar.gz" "$RUN_DIR/src.tar.gz" \
    || { status failed "could not download the source of release $target" source_download_failed "$(args release "$target")"; exit 1; }
actual="$(sha256sum "$RUN_DIR/src.tar.gz" | awk '{print $1}')"
[ "$actual" = "$src_sha" ] || { status failed "the source of release $target does not match its signed hash" source_hash_mismatch "$(args release "$target")"; exit 1; }
mkdir -p "$RUN_DIR/src" && tar -xzf "$RUN_DIR/src.tar.gz" -C "$RUN_DIR/src" --strip-components=1 \
    || { status failed "could not unpack release $target" source_unpack_failed "$(args release "$target")"; exit 1; }
# Checked and unpacked: nothing reads it again, and it holds RAM through
# the build.
rm -f "$RUN_DIR/src.tar.gz"

export OTC_VERIFIED_MANIFEST="$RUN_DIR/VERSIONS" OTC_VERIFIED_SRC="$RUN_DIR/src"
exec /bin/bash "$RUN_DIR/src/scripts/update.sh"
