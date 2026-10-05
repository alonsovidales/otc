#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# In-place device update (issue #94).
#
# Reads the version this device is on from /etc/otc/version, fetches the
# release manifest from GitHub, runs every release script after that one in
# order, then refreshes the binary and restarts the service.
#
# Launched as root by the device itself (see updater/updater.go) when the
# owner presses Update in Settings. It is deliberately a script rather than
# Go code: it has to outlive the process that started it, because the last
# thing it does is restart that very process.
#
# Everything here is safe to run twice. A run that dies halfway leaves
# /etc/otc/version on the last release that fully applied, so the next run
# picks up from exactly there - which is also why the version file is
# written after each script rather than once at the end.
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

mkdir -p "$(dirname "$STATUS_FILE")" "$(dirname "$LOG_FILE")" /etc/otc

# status <state> <message> - what the Settings screen reads back. Written
# to a file, not held in memory, because the service is restarted partway
# through and has to be able to report on an update it did not start.
# The directory is the otc user's, so root never opens or chmods a name
# it could have planted: dd's conv=excl creates the temp file with
# O_CREAT|O_EXCL (refusing a symlink already there), umask 022 gives it
# its final mode, and mv replaces a symlink rather than following it.
status() {
    local state="$1" message="$2" tmp="$STATUS_FILE.tmp.$$.$RANDOM$RANDOM"
    printf '{"state":%s,"message":%s,"version":%s,"updated":%s}\n' \
        "\"$state\"" "\"${message//\"/\\\"}\"" "\"$(cat "$VERSION_FILE" 2>/dev/null || echo unknown)\"" \
        "\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"" \
        | (umask 022; dd of="$tmp" conv=excl status=none 2>/dev/null) || { rm -f "$tmp"; return 1; }
    mv -Tf "$tmp" "$STATUS_FILE"
}

fail() {
    echo "ERROR: $1" >&2
    status failed "$1"
    exit 1
}

exec >>"$LOG_FILE" 2>&1
echo "=== update run $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="

status running "Checking for updates"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

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
    stage="$(mktemp -d /root/otc-update.XXXXXX)" || fail "no room to stage the update"
    printf '%s\n' "$RELEASE_KEY" > "$stage/key.pub"
    curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$stage/VERSIONS" "$REPO_RAW/scripts/updates/VERSIONS" \
        && curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$stage/VERSIONS.sig.b64" "$REPO_RAW/scripts/updates/VERSIONS.sig" \
        || fail "could not fetch the release manifest"
    base64 -d < "$stage/VERSIONS.sig.b64" > "$stage/VERSIONS.sig" 2>/dev/null \
        && openssl pkeyutl -verify -pubin -inkey "$stage/key.pub" -rawin -in "$stage/VERSIONS" -sigfile "$stage/VERSIONS.sig" >/dev/null 2>&1 \
        || fail "the release manifest is not signed with this project's release key - nothing was installed"
    boot_target=""; boot_src=""
    while IFS=$'\t' read -r version _s _a _d src; do
        case "$version" in ''|\#*) continue ;; esac
        boot_target="$version"; boot_src="$src"
    done < "$stage/VERSIONS"
    case "$boot_src" in [0-9a-f]*) ;; *) fail "release $boot_target has no signed source archive" ;; esac
    curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$stage/src.tar.gz" "$REPO_GH/releases/download/v$boot_target/src.tar.gz" \
        || fail "could not download the source of release $boot_target"
    [ "$(sha256sum "$stage/src.tar.gz" | awk '{print $1}')" = "$boot_src" ] \
        || fail "the source of release $boot_target does not match its signed hash"
    mkdir -p "$stage/src" && tar -xzf "$stage/src.tar.gz" -C "$stage/src" --strip-components=1 \
        || fail "could not unpack release $boot_target"
    export OTC_VERIFIED_MANIFEST="$stage/VERSIONS" OTC_VERIFIED_SRC="$stage/src"
    echo "verified release $boot_target (signature and source); continuing with its own updater"
    exec /bin/bash "$stage/src/scripts/update.sh"
fi

cp "$OTC_VERIFIED_MANIFEST" "$tmp/VERSIONS" || fail "the verified manifest is missing"

installed="$(cat "$VERSION_FILE" 2>/dev/null || echo 0)"
migrated="$(cat "$MIGRATED_FILE" 2>/dev/null || echo 0)"
[ "$migrated" -ge "$installed" ] 2>/dev/null || migrated="$installed"
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
    status uptodate "Already up to date"
    exit 0
fi

echo "pending releases: ${#pending[@]}"

for entry in "${pending[@]}"; do
    version="${entry%%$'\t'*}"
    sha="${entry##*$'\t'}"
    status running "Applying release $version"
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
        echo "$version" > "$MIGRATED_FILE"
        continue
    fi

    # From the verified source (every release keeps the earlier scripts),
    # and checked against the signed manifest before it runs as root.
    cp "$OTC_VERIFIED_SRC/scripts/updates/$version.sh" "$tmp/$version.sh" 2>/dev/null \
        || fail "release $version's script is not in the verified source"

    actual="$(sha256sum "$tmp/$version.sh" | awk '{print $1}')"
    if [ "$actual" != "$sha" ]; then
        fail "release $version failed its checksum (expected $sha, got $actual)"
    fi

    bash "$tmp/$version.sh" || fail "release $version failed to apply"

    # Written per release: an interrupted run then resumes after the last
    # script that actually completed.
    echo "$version" > "$MIGRATED_FILE"
    echo "release $version applied"
done

# Refreshing the code is common to every update, so it happens once here
# rather than in each release script.
# Pinned to the release's own tag rather than whatever main holds right
# now, so what gets built is exactly what this version is.
# The release's own source archive, already checked against the signed
# manifest by whoever started this script (see the top).
status running "Staging the source"
mkdir -p "$SRC_DIR"
rsync -a --delete --exclude '.git' "$OTC_VERIFIED_SRC/" "$SRC_DIR/" || fail "could not stage the new source"

cd "$SRC_DIR" || fail "no source directory at $SRC_DIR"
export PATH="/usr/local/bin:$PATH:/usr/local/go/bin"

# proto/generated is gitignored, so it is neither in the archive above nor
# left over from the previous build (the rsync --delete just removed it).
# Regenerated here exactly as install.sh does, from the proto that came
# with this very release - the first thing `go build` would otherwise hit
# is "undefined: pb.X". protoc-gen-go is pinned to go.mod's protobuf
# runtime so plugin and library stay compatible.
status running "Generating protobuf code"
PROTOC_VERSION=29.3
case "$(uname -m)" in
    aarch64|arm64) PROTOC_ARCH=aarch_64 ;;
    x86_64)        PROTOC_ARCH=x86_64 ;;
    *)             fail "unsupported architecture $(uname -m)" ;;
esac
# Pinned by hash (issue #160), like every download that ends up running.
case "$PROTOC_ARCH" in
    aarch_64) PROTOC_SHA=6427349140e01f06e049e707a58709a4f221ae73ab9a0425bc4a00c8d0e1ab32 ;;
    x86_64)   PROTOC_SHA=3e866620c5be27664f3d2fa2d656b5f3e09b5152b42f1bedbf427b333e90021a ;;
esac
if ! command -v protoc >/dev/null 2>&1 || ! protoc --version | grep -q " ${PROTOC_VERSION}$"; then
    curl -fsSL --retry 3 --retry-delay 2 "${CURL_STALL[@]}" -o "$tmp/protoc.zip" \
        "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${PROTOC_ARCH}.zip" \
        || fail "could not download protoc ${PROTOC_VERSION}"
    [ "$(sha256sum "$tmp/protoc.zip" | awk '{print $1}')" = "$PROTOC_SHA" ] || fail "protoc ${PROTOC_VERSION} does not match its pinned hash"
    rm -rf /opt/protoc && mkdir -p /opt/protoc
    (cd /opt/protoc && unzip -q "$tmp/protoc.zip") || fail "could not unpack protoc"
    ln -sf /opt/protoc/bin/protoc /usr/local/bin/protoc
fi
PROTOC_GEN_GO_VERSION="$(awk '/google.golang.org\/protobuf /{print $2}' go.mod)"
[ -n "$PROTOC_GEN_GO_VERSION" ] || fail "couldn't find google.golang.org/protobuf's version in go.mod"
GOBIN=/usr/local/bin go install "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}" \
    || fail "could not install protoc-gen-go ${PROTOC_GEN_GO_VERSION}"
mkdir -p proto/generated
protoc -I=proto --go_out=proto/generated --go_opt=paths=source_relative proto/messages.proto \
    || fail "protoc failed"

status running "Building"
# Same build environment as install.sh: CGO for gocv/ONNX Runtime, with
# the runtime's headers and library where install.sh put them.
export CGO_ENABLED=1
export CGO_CFLAGS="-I/opt/onnxruntime/include"
export CGO_LDFLAGS="-L/opt/onnxruntime/lib -lonnxruntime"
# Built into a temporary name and moved into place only on success: a
# failed build must leave the running binary alone rather than truncating
# the one the service needs to start again.
go build -o "$tmp/otc" ./bin/otc.go || fail "the build failed - see $LOG_FILE"

status running "Restarting"
install -m 0755 "$tmp/otc" /usr/bin/otc || fail "could not install the new binary"

# The other root-side scripts, which only install.sh used to put in place -
# so fixes to them (the symlink-safe status writes, the disk checks before a
# format) reach devices installed earlier. The update runner has already
# exec'd into this script, so replacing its file is safe.
install -m 0755 "$SRC_DIR/scripts/update-runner/otc-update-runner.sh" /usr/local/bin/otc-update-runner
# The update unit itself, which marks a run cut off part-way as failed
# rather than leaving it "running" (and the Update button locked) for good.
# Only install.sh put the unit in place before. The helper goes first: the
# unit names it.
if [ -f "$SRC_DIR/scripts/update-runner/otc-update-stopped.sh" ]; then
    install -m 0755 "$SRC_DIR/scripts/update-runner/otc-update-stopped.sh" /usr/local/bin/otc-update-stopped \
        && install -m 0644 "$SRC_DIR/scripts/update-runner/otc-update.service" /etc/systemd/system/otc-update.service \
        && systemctl daemon-reload
fi
# Issue #160: the key every later update's manifest must be signed with.
install -m 0644 "$SRC_DIR/scripts/release-signing.pub" /etc/otc/release-signing.pub
if [ -f "$SRC_DIR/scripts/raid_watch.py" ] && [ -f /usr/local/bin/raid_watch.py ]; then
    install -m 0755 "$SRC_DIR/scripts/raid_watch.py" /usr/local/bin/raid_watch.py
    systemctl try-restart raid-watch.service >/dev/null 2>&1 || true
fi

# Issue #145: the root side of switching the bridge on from Settings, for
# devices installed before it existed (install.sh sets it up on new ones).
# From the staged release, so it is only installed by a release that has it.
if [ -f "$SRC_DIR/scripts/bridge-runner/otc-bridge-runner.sh" ]; then
    install -m 0755 "$SRC_DIR/scripts/bridge-runner/otc-bridge-runner.sh" /usr/local/bin/otc-bridge-runner
    install -m 0644 "$SRC_DIR/scripts/bridge-runner/otc-bridge.service" /etc/systemd/system/otc-bridge.service
    install -m 0644 "$SRC_DIR/scripts/bridge-runner/otc-bridge.path" /etc/systemd/system/otc-bridge.path
    systemctl daemon-reload
    systemctl enable --now otc-bridge.path >/dev/null 2>&1 || echo "WARNING: could not enable otc-bridge.path"
fi
if [ -f "$SRC_DIR/scripts/tailscale-runner/otc-tailscale-runner.sh" ]; then
    install -m 0755 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale-runner.sh" /usr/local/bin/otc-tailscale-runner
    install -m 0644 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale.service" /etc/systemd/system/otc-tailscale.service
    install -m 0644 "$SRC_DIR/scripts/tailscale-runner/otc-tailscale.path" /etc/systemd/system/otc-tailscale.path
    systemctl daemon-reload
    systemctl enable --now otc-tailscale.path >/dev/null 2>&1 || echo "WARNING: could not enable otc-tailscale.path"
fi

# The web app ships prebuilt, attached to the release. Devices have no
# Node - the bundle is built once, by whoever cuts the release, rather
# than on every Raspberry Pi in existence. The binary is still built here,
# which is what keeps any architecture supported without a cross-build.
if [ "$target_assets_sha" != "-" ] && [ -n "$target_assets_sha" ]; then
    status running "Installing the web app"
    # Retried for a while: GitHub answers 500 now and then (release 88 on
    # Pit got four in a row).
    if curl -fsSL --retry 8 --retry-delay 5 --retry-all-errors "${CURL_STALL[@]}" -o "$tmp/web-dist.tar.gz" \
        "$REPO_GH/releases/download/v$target/web-dist.tar.gz"; then
        actual="$(sha256sum "$tmp/web-dist.tar.gz" | awk '{print $1}')"
        if [ "$actual" != "$target_assets_sha" ]; then
            fail "the web assets failed their checksum (expected $target_assets_sha, got $actual)"
        fi
        # Unpacked to one side and swapped in, so a half-extracted
        # archive can never be what the device is serving.
        rm -rf "$tmp/web-dist" && mkdir -p "$tmp/web-dist"
        tar -xzf "$tmp/web-dist.tar.gz" -C "$tmp/web-dist" || fail "could not unpack the web assets"
        rsync -a --delete "$tmp/web-dist/" /var/www/ || fail "could not install the web assets"
        # This runs as root, so the files land root-owned; hand them to the
        # service user, or a development `make web` (scp as otc) is refused
        # by the very files the previous release installed.
        chown -R otc:otc /var/www 2>/dev/null || true
        echo "web assets installed"
    else
        # The release has a web app (its hash is in the signed manifest),
        # it just couldn't be downloaded: fail, so the version stays where
        # it is and the next run tries again. Carrying on used to leave the
        # device on the new version with the old web app, for good.
        fail "could not download the web app for release $target - try the update again"
    fi
else
    echo "release $target ships no web assets, keeping the installed web app"
fi

# Everything worked: only now is this the version the device is on.
echo "$target" > "$VERSION_FILE"
status done "Updated to version $(cat "$VERSION_FILE")"
echo "=== update complete, restarting service ==="

# Last, and detached: this kills the process tree this script was started
# from, so nothing may follow it.
systemctl restart otc
