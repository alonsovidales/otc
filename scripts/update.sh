#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# In-place device update (issue #94).
#
# Reads the version this device is on from /etc/otc/version, fetches the
# release manifest from GitHub, runs every release script after that one in
# order, then refreshes the binary and restarts the service - and, if the
# new version doesn't start, puts the previous binary, web app and version
# back.
#
# Launched as root by the device itself (see updater/updater.go) when the
# owner presses Update in Settings. It is deliberately a script rather than
# Go code: it has to outlive the process that started it, because it
# restarts that very process (from otc-update.service, a unit of its own,
# it then watches the new version start).
#
# Everything here is safe to run twice. A run that dies halfway leaves
# /etc/otc/version on the last release that fully applied and
# /etc/otc/version.migrated on the last release whose script ran, so the
# next run picks up from exactly there.
set -uo pipefail

# Overridable so a fork, or a test run against a local checkout, doesn't
# need the script edited. Defaults to this project's own repository, which
# is the same place install.sh is fetched from and run as root.
#
# REPO_RAW serves the manifest and the release scripts, and is read from
# main so a device learns about a release the moment it is published.
# REPO_GH serves what a release actually installs - the source archive and
# the web assets - and is addressed by tag, so a device gets the artefacts
# belonging to the version it is moving to.
REPO_RAW="${OTC_REPO_RAW:-https://raw.githubusercontent.com/alonsovidales/otc/main}"
REPO_GH="${OTC_REPO_GH:-https://github.com/alonsovidales/otc}"
SRC_DIR=/opt/otc-src
VERSION_FILE=/etc/otc/version
# The last release whose migration script has run. The version itself is
# only written once the whole update has worked (build and web app
# included): written per release, a failed build or web download left the
# device on the new version with the old code and nothing left to retry.
MIGRATED_FILE=/etc/otc/version.migrated
STATUS_FILE=/var/lib/otc/update-status.json
# Root's own directory: /var/log/otc belongs to the otc user, and appending
# there as root would follow a symlink it put in place of the log.
LOG_FILE=/var/log/otc-update/update.log
# curl has no connect or stall timeout of its own: a transfer that stopped
# mid-way hung the update for good. Under 1 byte/s for two minutes is one
# that has truly stopped (exit 28, which --retry retries); a slow but live
# link is never cut off.
CURL_STALL=(--connect-timeout 30 --speed-limit 1 --speed-time 120)
# The web app the service serves (its [otc-api] static): a symlink to one
# directory under WEB_DIR, root's, one per release installed - the one
# served and the one before it, which a failed start goes back to.
WEB_ROOT=/var/www
WEB_DIR=/var/otc-web
# The binary the update replaces stays here until the next update: a new
# one that doesn't start is swapped back for it.
BIN=/usr/bin/otc
BIN_PREV=/usr/bin/otc.prev
# How long the new version has to start and answer, and how long it then
# has to keep running unchanged, before the update counts as done.
START_WAIT_S=180
START_STABLE_S=20

mkdir -p "$(dirname "$STATUS_FILE")" "$(dirname "$LOG_FILE")" /etc/otc

# read_number <file>: the release number in a version file, or nothing
# (status 1) when it is absent, empty or not a number - a write cut short
# by a power cut leaves such a file.
read_number() {
    local v
    v="$(head -c 32 "$1" 2>/dev/null | head -n 1 | tr -d '[:space:]')"
    [[ "$v" =~ ^[0-9]{1,9}$ ]] || return 1
    printf '%s' "$((10#$v))"
}

# status <state> <message> [<code> [<args>]] - what the Settings screen
# reads back. Written to a file, not held in memory, because the service is
# restarted partway through and has to be able to report on an update it
# did not start. message is the English that devices from before codes
# show; code names the same text in the device's catalog
# (dev.update.<code>, devtext's table) and args, from args() below, its
# arguments - so a newer device shows it in its owner's language
# (docs/i18n.md, "Text written before any request").
# The directory is the otc user's, so root never opens or chmods a name
# it could have planted: dd's conv=excl creates the temp file with
# O_CREAT|O_EXCL (refusing a symlink already there), umask 022 gives it
# its final mode, and mv replaces a symlink rather than following it.
status() {
    local state="$1" message="$2" code="${3:-}" code_args="${4:-}" tmp="$STATUS_FILE.tmp.$$.$RANDOM$RANDOM"
    [ -n "$code_args" ] || code_args='{}'
    printf '{"state":%s,"message":%s,"code":%s,"args":%s,"version":%s,"updated":%s}\n' \
        "\"$state\"" "\"${message//\"/\\\"}\"" "\"$code\"" "$code_args" "\"$(read_number "$VERSION_FILE" || echo unknown)\"" \
        "\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"" \
        | (umask 022; dd of="$tmp" conv=excl status=none 2>/dev/null) || { rm -f "$tmp"; return 1; }
    mv -Tf "$tmp" "$STATUS_FILE"
}

# args <name> <value>...: a status code's arguments as a JSON object of
# strings. Backslashes and quotes are escaped and control characters
# dropped, so no value can leave its string.
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

# fail <message> [<code> [<args>]]: as status.
fail() {
    echo "ERROR: $1" >&2
    status failed "$1" "${2:-}" "${3:-}"
    exit 1
}

# install_atomic <mode> <src> <dst>: written beside the destination,
# flushed, and renamed over it. `install` unlinks and rewrites in place, so
# a power cut before writeback could leave an empty /usr/bin/otc - a
# service that never starts again, on a device whose only way to update is
# that very service.
install_atomic() {
    local n="$3.new.$$"
    install -m "$1" "$2" "$n" && { sync "$n" 2>/dev/null || sync; } && mv -Tf "$n" "$3" || { rm -f "$n"; return 1; }
}

# write_atomic <file> <value>: same, for the version files (an empty one
# can't be read: see read_number and the version check below).
write_atomic() {
    printf '%s\n' "$2" > "$1.tmp.$$" && { sync "$1.tmp.$$" 2>/dev/null || sync; } && mv -Tf "$1.tmp.$$" "$1" \
        || { rm -f "$1.tmp.$$"; return 1; }
}

# free_space <path> <MB>: whether the filesystem holding path has MB
# megabytes free; free_h and need_h say how much in words, for a message.
free_space() {
    local kb
    kb="$(df -Pk "$1" 2>/dev/null | awk 'NR == 2 {print $4}')"
    [[ "$kb" =~ ^[0-9]+$ ]] || return 0 # can't tell: the update finds out
    free_h="$(awk -v k="$kb" 'BEGIN { if (k >= 1048576) printf "%.1f GB", k / 1048576; else printf "%d MB", k / 1024 }')"
    need_h="$(awk -v m="$2" 'BEGIN { if (m >= 1024) printf "%.1f GB", m / 1024; else printf "%d MB", m }')"
    [ "$kb" -ge $(($2 * 1024)) ]
}

# manifest_verified <manifest> <signature, base64> <key>: the manifest
# carries the release key's signature.
manifest_verified() {
    base64 -d < "$2" > "$2.raw" 2>/dev/null \
        && openssl pkeyutl -verify -pubin -inkey "$3" -rawin -in "$1" -sigfile "$2.raw" >/dev/null 2>&1
}

# ini_get <file> <section> <key>: a value from the device's config.
ini_get() {
    awk -F= -v section="[$2]" -v key="$3" '
        /^[[:space:]]*\[/ { s = $0; gsub(/[[:space:]]/, "", s); next }
        s == section {
            k = $1; gsub(/[[:space:]]/, "", k)
            if (k == key) { v = substr($0, index($0, "=") + 1); gsub(/[[:space:]]/, "", v); print v; exit }
        }' "$1" 2>/dev/null
}

# otc_answers: the service runs and answers its local health check
# (api's /check_healty, plain HTTP on [otc-api] port).
otc_answers() {
    systemctl is-active --quiet otc.service \
        && [ "$(curl -fsS --noproxy '*' --max-time 5 "http://127.0.0.1:$health_port/check_healty" 2>/dev/null)" = OK ]
}

# otc_started <seconds>: within that long the service answers, and then
# keeps answering for START_STABLE_S as the same process, never restarted
# in between: a binary that fails at start never answers, and
# Restart=on-failure would otherwise start it again every few seconds for
# good. The service's own users (the supervisor's child instances) follow
# it: they run the same binary, and stop and start with it.
otc_started() {
    local deadline=$((SECONDS + $1)) pid restarts
    while [ "$SECONDS" -lt "$deadline" ]; do
        if otc_answers; then
            pid="$(systemctl show -p MainPID --value otc.service 2>/dev/null)"
            restarts="$(systemctl show -p NRestarts --value otc.service 2>/dev/null)"
            sleep "$START_STABLE_S"
            if [ -n "$pid" ] && [ "$pid" != 0 ] && otc_answers \
                && [ "$(systemctl show -p MainPID --value otc.service 2>/dev/null)" = "$pid" ] \
                && [ "$(systemctl show -p NRestarts --value otc.service 2>/dev/null)" = "$restarts" ]; then
                return 0
            fi
        fi
        sleep 3
    done
    return 1
}

# web_link <dir>: /var/www names dir, in one rename - a symlink written
# beside it and moved over it.
web_link() {
    ln -sfn "$1" "$WEB_DIR/.www.$$" && mv -Tf "$WEB_DIR/.www.$$" "$WEB_ROOT" || { rm -f "$WEB_DIR/.www.$$"; return 1; }
}

exec >>"$LOG_FILE" 2>&1
echo "=== update run $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="

# Held (shared) on fd 9 for as long as this run lasts, kept across the
# bootstrap's exec below. The device reads "running" with
# otc-update.service stopped as a run that was cut off, and a run started
# by hand (the README's console line, the legacy sudo path, a shell on a
# dev box) runs outside that unit: this lock is how it tells such a live
# run from a dead one (updater.updateUnitGone). The kernel drops it with
# the run however that ends, and /run starts empty after a reboot. Shared,
# so it never holds a run up; in root's /run, so nothing the otc user
# planted is opened.
RUN_LOCK=/run/otc-update.lock
if ! [ /proc/self/fd/9 -ef "$RUN_LOCK" ]; then
    (umask 022; : >> "$RUN_LOCK") 2>/dev/null && exec 9<"$RUN_LOCK"
fi
flock -s -w 10 9 2>/dev/null || true

status running "Checking for updates" checking

# The verified release this run came from, staged by the runner in /run
# (tmpfs, so RAM) or by the bootstrap below in /root: nothing reads it once
# the run is over, and nothing else ever removed it. Only those two shapes
# are removed, never whatever the variable might hold.
cleanup_stage() {
    case "${OTC_VERIFIED_SRC:-}" in
        /run/otc-update/src) rm -rf /run/otc-update ;;
        /root/otc-update.*/src) rm -rf "${OTC_VERIFIED_SRC%/src}" ;;
    esac
}

tmp="$(mktemp -d)"
# The web app's new directory once unpacked, and whether it was switched
# in: one never switched in is removed with the rest.
web_new=""
web_switched=""
# ${stage:+...}: the bootstrap's own failures before it hands over (its
# exec drops this trap; the update.sh it hands over to cleans up instead).
trap 'rm -rf "$tmp" ${stage:+"$stage"}; [ -z "$web_new" ] || [ -n "$web_switched" ] || rm -rf "$web_new"; cleanup_stage' EXIT

# Issue #160: this runs only from a verified release. otc-update-runner
# checks the manifest's signature and the release's source archive and
# starts the update.sh inside that archive, with OTC_VERIFIED_MANIFEST and
# OTC_VERIFIED_SRC set. An updater from before signed releases instead ran
# this file straight from main: then this copy does the same checks itself,
# with the release key below (the one time a device has to trust main), and
# hands over to the verified release's own update.sh - which installs the
# key (/etc/otc/release-signing.pub) and the new runner for every update
# after.
RELEASE_KEY='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAtVgLIKBzcqMNM2nUnK9xfgpqWrLTuZsk8ylhyI0BK9g=
-----END PUBLIC KEY-----'
if [ -z "${OTC_VERIFIED_SRC:-}" ] || [ -z "${OTC_VERIFIED_MANIFEST:-}" ]; then
    free_space /root 1024 || fail "not enough free space for the update: /root has $free_h free, it needs $need_h" no_space "$(args path "/root" free "$free_h" needed "$need_h")"
    stage="$(mktemp -d /root/otc-update.XXXXXX)" || fail "no room to stage the update" no_room
    printf '%s\n' "$RELEASE_KEY" > "$stage/key.pub"
    # Right after a release the CDN can serve the new manifest beside the
    # old signature for a while: a signature that doesn't match is fetched
    # again for about a minute before the update gives up.
    for try in 1 2 3 4 5 6 7; do
        curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$stage/VERSIONS" "$REPO_RAW/scripts/updates/VERSIONS" \
            && curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$stage/VERSIONS.sig.b64" "$REPO_RAW/scripts/updates/VERSIONS.sig" \
            || fail "could not fetch the release manifest" manifest_fetch_failed
        manifest_verified "$stage/VERSIONS" "$stage/VERSIONS.sig.b64" "$stage/key.pub" && break
        [ "$try" -lt 7 ] || fail "could not verify this release yet - nothing was installed, try again in a few minutes" manifest_not_signed
        echo "the release manifest's signature doesn't match (yet): fetching it again in 10 s"
        sleep 10
    done
    boot_target=""; boot_src=""
    while IFS=$'\t' read -r version _s _a _d src; do
        case "$version" in ''|\#*) continue ;; esac
        boot_target="$version"; boot_src="$src"
    done < "$stage/VERSIONS"
    case "$boot_src" in [0-9a-f]*) ;; *) fail "release $boot_target has no signed source archive" no_source_archive "$(args release "$boot_target")" ;; esac
    curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$stage/src.tar.gz" "$REPO_GH/releases/download/v$boot_target/src.tar.gz" \
        || fail "could not download the source of release $boot_target" source_download_failed "$(args release "$boot_target")"
    [ "$(sha256sum "$stage/src.tar.gz" | awk '{print $1}')" = "$boot_src" ] \
        || fail "the source of release $boot_target does not match its signed hash" source_hash_mismatch "$(args release "$boot_target")"
    mkdir -p "$stage/src" && tar -xzf "$stage/src.tar.gz" -C "$stage/src" --strip-components=1 \
        || fail "could not unpack release $boot_target" source_unpack_failed "$(args release "$boot_target")"
    export OTC_VERIFIED_MANIFEST="$stage/VERSIONS" OTC_VERIFIED_SRC="$stage/src"
    echo "verified release $boot_target (signature and source); continuing with its own updater"
    exec /bin/bash "$stage/src/scripts/update.sh"
fi

cp "$OTC_VERIFIED_MANIFEST" "$tmp/VERSIONS" || fail "the verified manifest is missing" verified_manifest_missing

# No version file is a device from before updates existed: release 0, the
# whole history (every script is idempotent). One that can't be read - a
# write cut short - is not 0, nor "up to date" for good, as it used to
# read: the version comes back from version.migrated, which every update
# writes before the version file (and install.sh with it), and is written
# again; with neither readable the update stops and says so.
migrated="$(read_number "$MIGRATED_FILE" || true)"
if [ ! -e "$VERSION_FILE" ]; then
    installed=0
elif installed="$(read_number "$VERSION_FILE")"; then
    :
elif [ -n "$migrated" ]; then
    installed="$migrated"
    echo "WARNING: $VERSION_FILE can't be read: release $installed recovered from $MIGRATED_FILE"
    write_atomic "$VERSION_FILE" "$installed" || echo "WARNING: could not write $VERSION_FILE again"
else
    fail "this device's version file ($VERSION_FILE) can't be read - reinstall or update by hand" version_unreadable "$(args path "$VERSION_FILE")"
fi
[ -n "$migrated" ] && [ "$migrated" -ge "$installed" ] || migrated="$installed"
echo "installed version: $installed"

# Releases are listed oldest first; anything numerically after what is
# installed is pending. Comments and blank lines are skipped.
pending=()
target=""
target_assets_sha=""
while IFS=$'\t' read -r version script_sha assets_sha summary _src_sha; do
    case "$version" in ''|\#*) continue ;; esac
    if [ "$version" -gt "$installed" ] 2>/dev/null; then
        pending+=("$version"$'\t'"$script_sha")
        # The last pending release is the one this device is moving to,
        # and therefore the one whose source and assets get installed.
        target="$version"
        target_assets_sha="$assets_sha"
    fi
done < "$tmp/VERSIONS"

if [ "${#pending[@]}" -eq 0 ]; then
    echo "already up to date"
    status uptodate "Already up to date" uptodate
    exit 0
fi

echo "pending releases: ${#pending[@]}"

# Before anything is changed: the build (Go's caches under /root, a new
# toolchain when go.mod asks for one, the source in /opt), the new binary
# beside the old one and the web app take up to about 1 GB of the system
# disk; the build's own work and the downloads in the temporary directory
# (tmpfs on Raspberry Pi OS 13) up to about 300 MB. A full SD card used to
# fail half-way, in the build.
free_space / 1024 || fail "not enough free space for the update: / has $free_h free, it needs $need_h" no_space "$(args path "/" free "$free_h" needed "$need_h")"
free_space "$tmp" 300 || fail "not enough free space for the update: $tmp has $free_h free, it needs $need_h" no_space "$(args path "$tmp" free "$free_h" needed "$need_h")"

for entry in "${pending[@]}"; do
    version="${entry%%$'\t'*}"
    sha="${entry##*$'\t'}"
    status running "Applying release $version" applying "$(args release "$version")"
    echo "--- release $version ---"

    # Already run by an update that then failed later on (the build, the
    # web app): scripts are idempotent, but there is no need.
    if [ "$version" -le "$migrated" ] 2>/dev/null; then
        echo "release $version was already applied"
        continue
    fi

    # Most releases change no schema and carry no script at all.
    if [ "$sha" = "-" ]; then
        echo "release $version has no migration"
        write_atomic "$MIGRATED_FILE" "$version"
        continue
    fi

    # From the verified source (every release keeps the earlier scripts),
    # and checked against the signed manifest before it runs as root.
    cp "$OTC_VERIFIED_SRC/scripts/updates/$version.sh" "$tmp/$version.sh" 2>/dev/null \
        || fail "release $version's script is not in the verified source" script_missing "$(args release "$version")"

    actual="$(sha256sum "$tmp/$version.sh" | awk '{print $1}')"
    if [ "$actual" != "$sha" ]; then
        fail "release $version failed its checksum (expected $sha, got $actual)" script_checksum "$(args release "$version" expected "$sha" actual "$actual")"
    fi

    bash "$tmp/$version.sh" || fail "release $version failed to apply" script_failed "$(args release "$version")"

    # Written per release: an interrupted run then resumes after the last
    # script that actually completed.
    write_atomic "$MIGRATED_FILE" "$version"
    echo "release $version applied"
done

# On this day's photo quality model (release 150): optional, so a device
# whose download failed - at release 150's script or since - gets it from
# the next update that reaches it. Only while it is missing, checked against
# its pin (the same in install.sh, updates/150.sh and docker/entrypoint.sh),
# into a temporary file beside it, then renamed; a failure only warns.
QUALITY_ONNX=/usr/local/models/nima_mobilenet_aesthetic.onnx
QUALITY_ONNX_URL=https://github.com/alonsovidales/otc/releases/download/models/nima_mobilenet_aesthetic.onnx
QUALITY_ONNX_SHA=b35a4d6616b5597fb84d57cbcfbc7d5f3d80b694069d1182b66677903d0b71d1
if [ -d /usr/local/models ] && [ ! -L /usr/local/models ] && [ ! -e "$QUALITY_ONNX" ] && [ ! -L "$QUALITY_ONNX" ]; then
    if qtmp="$(mktemp -d /usr/local/models/.otc-fetch.XXXXXX)"; then
        if curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$qtmp/download" "$QUALITY_ONNX_URL" \
            && [ "$(sha256sum "$qtmp/download" | awk '{print $1}')" = "$QUALITY_ONNX_SHA" ]; then
            chmod 0644 "$qtmp/download"
            mv -Tf "$qtmp/download" "$QUALITY_ONNX"
            echo "fetched the photo quality model"
        else
            echo "warning: the photo quality model could not be fetched - photos are measured without it until the next update"
        fi
        rm -rf -- "$qtmp"
    fi
fi

# Refreshing the code is common to every update, so it happens once here
# rather than in each release script.
# Pinned to the release's own tag rather than whatever main holds right
# now, so what gets built is exactly what this version is.
# The release's own source archive, already checked against the signed
# manifest by whoever started this script (see the top).
status running "Staging the source" staging
mkdir -p "$SRC_DIR"
rsync -a --delete --exclude '.git' "$OTC_VERIFIED_SRC/" "$SRC_DIR/" || fail "could not stage the new source" stage_failed

cd "$SRC_DIR" || fail "no source directory at $SRC_DIR" no_source_dir "$(args path "$SRC_DIR")"
export PATH="/usr/local/bin:$PATH:/usr/local/go/bin"

# proto/generated is gitignored, so it is neither in the archive above nor
# left over from the previous build (the rsync --delete just removed it).
# Regenerated here exactly as install.sh does, from the proto that came
# with this very release - the first thing `go build` would otherwise hit
# is "undefined: pb.X". protoc-gen-go is pinned to go.mod's protobuf
# runtime so plugin and library stay compatible.
status running "Generating protobuf code" generating_protobuf
PROTOC_VERSION=29.3
case "$(uname -m)" in
    aarch64|arm64) PROTOC_ARCH=aarch_64 ;;
    x86_64)        PROTOC_ARCH=x86_64 ;;
    *)             arch="$(uname -m)"; fail "unsupported architecture $arch" unsupported_arch "$(args arch "$arch")" ;;
esac
# Pinned by hash (issue #160), like every download that ends up running.
case "$PROTOC_ARCH" in
    aarch_64) PROTOC_SHA=6427349140e01f06e049e707a58709a4f221ae73ab9a0425bc4a00c8d0e1ab32 ;;
    x86_64)   PROTOC_SHA=3e866620c5be27664f3d2fa2d656b5f3e09b5152b42f1bedbf427b333e90021a ;;
esac
if ! command -v protoc >/dev/null 2>&1 || ! protoc --version | grep -q " ${PROTOC_VERSION}$"; then
    curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$tmp/protoc.zip" \
        "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${PROTOC_ARCH}.zip" \
        || fail "could not download protoc ${PROTOC_VERSION}" protoc_download_failed "$(args version "$PROTOC_VERSION")"
    [ "$(sha256sum "$tmp/protoc.zip" | awk '{print $1}')" = "$PROTOC_SHA" ] || fail "protoc ${PROTOC_VERSION} does not match its pinned hash" protoc_hash_mismatch "$(args version "$PROTOC_VERSION")"
    rm -rf /opt/protoc && mkdir -p /opt/protoc
    (cd /opt/protoc && unzip -q "$tmp/protoc.zip") || fail "could not unpack protoc" protoc_unpack_failed
    ln -sf /opt/protoc/bin/protoc /usr/local/bin/protoc
fi
PROTOC_GEN_GO_VERSION="$(awk '/google.golang.org\/protobuf /{print $2}' go.mod)"
[ -n "$PROTOC_GEN_GO_VERSION" ] || fail "couldn't find google.golang.org/protobuf's version in go.mod" protobuf_version_missing
GOBIN=/usr/local/bin go install "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}" \
    || fail "could not install protoc-gen-go ${PROTOC_GEN_GO_VERSION}" protoc_gen_go_failed "$(args version "$PROTOC_GEN_GO_VERSION")"
mkdir -p proto/generated
protoc -I=proto --go_out=proto/generated --go_opt=paths=source_relative proto/messages.proto \
    || fail "protoc failed" protoc_failed

status running "Building" building
# Same build environment as install.sh: CGO for gocv/ONNX Runtime, with
# the runtime's headers and library where install.sh put them.
export CGO_ENABLED=1
export CGO_CFLAGS="-I/opt/onnxruntime/include"
export CGO_LDFLAGS="-L/opt/onnxruntime/lib -lonnxruntime"
# Built into a temporary name and moved into place only on success: a
# failed build must leave the running binary alone rather than truncating
# the one the service needs to start again.
#
# One compile at a time on a device with less than 6 GB of memory: the
# build runs next to the service, and a cold cache compiles gocv's C++ with
# a compiler per core - on a 4 GB Pi that alone can run out of memory.
build_jobs=""
mem_kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || true)"
if [ -n "$mem_kb" ] && [ "$mem_kb" -lt $((6 * 1024 * 1024)) ]; then
    build_jobs="-p 1"
    echo "less than 6 GB of memory: building one package at a time"
fi
# shellcheck disable=SC2086 # build_jobs is empty or two words on purpose
go build $build_jobs -o "$tmp/otc" ./bin/otc.go || fail "the build failed - see $LOG_FILE" build_failed "$(args path "$LOG_FILE")"

# The web app ships prebuilt, attached to the release. Devices have no
# Node - the bundle is built once, by whoever cuts the release, rather
# than on every Raspberry Pi in existence. The binary is still built here,
# which is what keeps any architecture supported without a cross-build.
# Downloaded, checked and unpacked before anything is installed: a failure
# here leaves the binary, the runners and the units exactly as they were,
# rather than a new binary that the next restart would run against the
# old web app and the old recorded version.
web_staged=""
if [ "$target_assets_sha" != "-" ] && [ -n "$target_assets_sha" ]; then
    status running "Installing the web app" installing_web
    # Retried for a while: GitHub answers 500 now and then (release 88 on
    # Pit got four in a row).
    if curl -fsSL --retry 8 --retry-delay 5 --retry-all-errors "${CURL_STALL[@]}" -o "$tmp/web-dist.tar.gz" \
        "$REPO_GH/releases/download/v$target/web-dist.tar.gz"; then
        actual="$(sha256sum "$tmp/web-dist.tar.gz" | awk '{print $1}')"
        if [ "$actual" != "$target_assets_sha" ]; then
            fail "the web assets failed their checksum (expected $target_assets_sha, got $actual)" web_checksum "$(args expected "$target_assets_sha" actual "$actual")"
        fi
        # Unpacked into a directory of its own beside the one served, and
        # switched in below by renaming the /var/www symlink: a
        # half-extracted archive is never what the device serves, and root
        # never writes into a tree the service owns. Not served until then
        # (the exit trap removes it if the update stops before).
        mkdir -p "$WEB_DIR" && chown root:root "$WEB_DIR" && chmod 0755 "$WEB_DIR" \
            && web_new="$(mktemp -d "$WEB_DIR/$target.XXXXXX")" \
            || fail "could not unpack the web assets" web_unpack_failed
        tar --no-same-owner -xzf "$tmp/web-dist.tar.gz" -C "$web_new" || fail "could not unpack the web assets" web_unpack_failed
        # The service's, as before (a development `make web` copies into
        # it as otc); the directory above it stays root's.
        chown -R otc:otc "$web_new" && chmod 0755 "$web_new" || fail "could not unpack the web assets" web_unpack_failed
        web_staged=1
    else
        # The release has a web app (its hash is in the signed manifest),
        # it just couldn't be downloaded: fail, so the version stays where
        # it is and the next run tries again. Carrying on used to leave the
        # device on the new version with the old web app, for good.
        fail "could not download the web app for release $target - try the update again" web_download_failed "$(args release "$target")"
    fi
else
    echo "release $target ships no web assets, keeping the installed web app"
fi

status running "Restarting" restarting
# The binary being replaced stays as otc.prev (a hard link: the same file,
# kept by a second name, renamed into place), so a new one that doesn't
# start can be swapped back for it. After a rollback the two names already
# are the same file (and mv refuses to rename one onto the other).
if [ -f "$BIN" ] && ! [ "$BIN" -ef "$BIN_PREV" ]; then
    { ln -f "$BIN" "$BIN_PREV.$$" && mv -Tf "$BIN_PREV.$$" "$BIN_PREV"; } \
        || { rm -f "$BIN_PREV.$$"; fail "could not install the new binary" binary_install_failed; }
fi
install_atomic 0755 "$tmp/otc" "$BIN" || fail "could not install the new binary" binary_install_failed

# The other root-side scripts, which only install.sh used to put in place -
# so fixes to them (the symlink-safe status writes, the disk checks before a
# format) reach devices installed earlier. The update runner has already
# exec'd into this script, so replacing its file is safe.
install_atomic 0755 "$SRC_DIR/scripts/update-runner/otc-update-runner.sh" /usr/local/bin/otc-update-runner
# The update unit itself, which marks a run cut off part-way as failed
# rather than leaving it "running" (and the Update button locked) for good.
# Only install.sh put the unit in place before. The helper goes first: the
# unit names it.
if [ -f "$SRC_DIR/scripts/update-runner/otc-update-stopped.sh" ]; then
    install_atomic 0755 "$SRC_DIR/scripts/update-runner/otc-update-stopped.sh" /usr/local/bin/otc-update-stopped \
        && install_atomic 0644 "$SRC_DIR/scripts/update-runner/otc-update.service" /etc/systemd/system/otc-update.service \
        && systemctl daemon-reload
fi
# Issue #160: the key every later update's manifest must be signed with.
install_atomic 0644 "$SRC_DIR/scripts/release-signing.pub" /etc/otc/release-signing.pub
if [ -f "$SRC_DIR/scripts/raid_watch.py" ] && [ -f /usr/local/bin/raid_watch.py ]; then
    install_atomic 0755 "$SRC_DIR/scripts/raid_watch.py" /usr/local/bin/raid_watch.py
    systemctl try-restart raid-watch.service >/dev/null 2>&1 || true
fi
# The image's network script (root): only on a device made from the image,
# which has it. Release 143 put the current one on devices imaged before
# 2026-09-29, whose copy raised the open setup hotspot whenever the
# network was lost; from then on every update keeps it current.
if [ -f "$SRC_DIR/scripts/network_setup.py" ] && [ -f /usr/local/bin/network_setup.py ]; then
    install_atomic 0755 "$SRC_DIR/scripts/network_setup.py" /usr/local/bin/network_setup.py
    systemctl try-restart network-setup.service >/dev/null 2>&1 || true
fi

# Issue #145: the root side of switching the bridge on from Settings, for
# devices installed before it existed (install.sh sets it up on new ones).
# From the staged release, so it is only installed by a release that has it.
if [ -f "$SRC_DIR/scripts/bridge-runner/otc-bridge-runner.sh" ]; then
    install_atomic 0755 "$SRC_DIR/scripts/bridge-runner/otc-bridge-runner.sh" /usr/local/bin/otc-bridge-runner
    install_atomic 0644 "$SRC_DIR/scripts/bridge-runner/otc-bridge.service" /etc/systemd/system/otc-bridge.service
    install_atomic 0644 "$SRC_DIR/scripts/bridge-runner/otc-bridge.path" /etc/systemd/system/otc-bridge.path
    systemctl daemon-reload
    systemctl enable --now otc-bridge.path >/dev/null 2>&1 || echo "WARNING: could not enable otc-bridge.path"
fi
if [ -f "$SRC_DIR/scripts/tailscale-runner/otc-tailscale-runner.sh" ]; then
    install_atomic 0755 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale-runner.sh" /usr/local/bin/otc-tailscale-runner
    install_atomic 0644 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale.service" /etc/systemd/system/otc-tailscale.service
    install_atomic 0644 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale.path" /etc/systemd/system/otc-tailscale.path
    systemctl daemon-reload
    systemctl enable --now otc-tailscale.path >/dev/null 2>&1 || echo "WARNING: could not enable otc-tailscale.path"
fi
# Settings > Restart device and the Wi-Fi watchdog's restart (release
# 115, which installs them first; reinstalled here so later fixes reach
# every device).
if [ -f "$SRC_DIR/scripts/device-runner/otc-wifi-restart-runner.sh" ]; then
    for unit in reboot wifi-restart; do
        install_atomic 0755 "$SRC_DIR/scripts/device-runner/otc-$unit-runner.sh" "/usr/local/bin/otc-$unit-runner"
        install_atomic 0644 "$SRC_DIR/scripts/device-runner/otc-$unit.service" "/etc/systemd/system/otc-$unit.service"
        install_atomic 0644 "$SRC_DIR/scripts/device-runner/otc-$unit.path" "/etc/systemd/system/otc-$unit.path"
    done
    systemctl daemon-reload
    for unit in reboot wifi-restart; do
        systemctl enable --now "otc-$unit.path" >/dev/null 2>&1 || echo "WARNING: could not enable otc-$unit.path"
    done
fi
# Settings > Wi-Fi's root side (release 140, which installs it first): the
# runner is Python, so it has its own lines. Every device that updates gets
# it, image or hand-installed; it acts only where NetworkManager runs.
if [ -f "$SRC_DIR/scripts/device-runner/otc-wifi-join-runner.py" ]; then
    install_atomic 0755 "$SRC_DIR/scripts/device-runner/otc-wifi-join-runner.py" /usr/local/bin/otc-wifi-join-runner
    install_atomic 0644 "$SRC_DIR/scripts/device-runner/otc-wifi-join.service" /etc/systemd/system/otc-wifi-join.service
    install_atomic 0644 "$SRC_DIR/scripts/device-runner/otc-wifi-join.path" /etc/systemd/system/otc-wifi-join.path
    systemctl daemon-reload
    # A request still there now is one no runner took (its path unit
    # wasn't watching): dropped, and the device answers no_answer for it.
    # The device refuses a change while an update runs, so none is new.
    rm -f /run/otc/wifi-join.request
    systemctl enable --now otc-wifi-join.path >/dev/null 2>&1 || echo "WARNING: could not enable otc-wifi-join.path"
fi

# The web app: the new directory switched in with one rename of the
# /var/www symlink. The one it replaces is kept to go back to; the very
# first time, the directory earlier releases wrote into file by file moves
# under /var/otc-web and /var/www becomes the symlink.
web_prev=""
if [ -n "$web_staged" ]; then
    if [ -L "$WEB_ROOT" ]; then
        web_prev="$(readlink -f "$WEB_ROOT" || true)"
    elif [ -d "$WEB_ROOT" ]; then
        web_prev="$WEB_DIR/old.$(date -u +%Y%m%d%H%M%S)"
        mv -T "$WEB_ROOT" "$web_prev" || fail "could not install the web assets" web_install_failed
        sync
    fi
    web_link "$web_new" || {
        # Never left without one.
        [ -z "$web_prev" ] || [ -e "$WEB_ROOT" ] || web_link "$web_prev" || true
        fail "could not install the web assets" web_install_failed
    }
    web_switched=1
    echo "web assets installed"
fi

# Everything is installed: this is the version the device is on, unless it
# doesn't start (below).
write_atomic "$VERSION_FILE" "$target" || fail "could not install the new binary" binary_install_failed
updated="$(read_number "$VERSION_FILE" || echo "$target")"
# On disk before the restart: the binary, the web app, the units and the
# version files, so a power cut right after can't undo half of them.
sync

# Run by the legacy sudo path (a device set up by hand before the update
# unit existed), this script is one of the service's own processes: the
# restart ends it, so it reports first and restarts last, as it always did,
# with nothing to check the start with.
if grep -q '/otc\.service$' /proc/self/cgroup 2>/dev/null; then
    status done "Updated to version $updated" updated "$(args version "$updated")"
    echo "=== update complete, restarting service ==="
    systemctl restart otc
    exit 0
fi

# Its own unit (otc-update.service), or a shell: the restart leaves this
# script running, and it waits for the new version to start and answer
# before calling the update done.
health_port="$(ini_get "/etc/otc_$(systemctl show -p ExecStart --value otc.service 2>/dev/null \
    | sed -n 's/.*argv\[\]=\/usr\/bin\/otc \([^ ;]*\).*/\1/p' | head -1).ini" otc-api port)"
[[ "$health_port" =~ ^[0-9]{1,5}$ ]] || health_port=8080
echo "=== restarting the service, waiting for release $target to start ==="
systemctl restart otc
status running "Checking that the new version starts" verifying
if otc_started "$START_WAIT_S"; then
    status done "Updated to version $updated" updated "$(args version "$updated")"
    echo "=== update complete: release $target started ==="
    # The web app served and the one before it stay; older ones go.
    if [ -n "$web_switched" ]; then
        for d in "$WEB_DIR"/*; do
            [ -d "$d" ] && [ ! -L "$d" ] || continue
            [ "$d" -ef "$web_new" ] && continue
            [ -n "$web_prev" ] && [ "$d" -ef "$web_prev" ] && continue
            rm -rf -- "$d"
        done
    fi
    exit 0
fi

# It didn't start: the binary, the web app and the version it replaced go
# back, and the service is restarted on them. The migrations stay (they
# only ever add, so the previous binary runs on them) and so does
# version.migrated: pressing Update again builds the new version again
# without re-running them. The runners and units stay too: they work with
# either version.
echo "ERROR: release $target did not start within ${START_WAIT_S}s - putting back release $installed"
systemctl status otc.service --no-pager -n 30 2>&1 || true
put_back=1
if [ -f "$BIN_PREV" ] && [ "$BIN" -ef "$BIN_PREV" ]; then
    : # already the previous one
elif [ -f "$BIN_PREV" ]; then
    { ln -f "$BIN_PREV" "$BIN.rollback.$$" && mv -Tf "$BIN.rollback.$$" "$BIN"; } || { rm -f "$BIN.rollback.$$"; put_back=""; }
else
    put_back=""
fi
if [ -n "$web_switched" ]; then
    if [ -n "$web_prev" ] && [ -d "$web_prev" ] && web_link "$web_prev"; then
        rm -rf -- "$web_new"
    else
        put_back=""
    fi
fi
if [ "$installed" -gt 0 ]; then
    write_atomic "$VERSION_FILE" "$installed" || put_back=""
else
    rm -f "$VERSION_FILE" || put_back=""
fi
sync
systemctl restart otc
if [ -z "$put_back" ]; then
    fail "the new version didn't start, and the previous one couldn't be put back - see $LOG_FILE" rollback_failed "$(args path "$LOG_FILE")"
fi
if otc_started "$START_WAIT_S"; then
    echo "release $installed is running again"
else
    echo "WARNING: release $installed was put back but doesn't answer either"
fi
fail "the new version didn't start, so the previous one was put back" rolled_back
