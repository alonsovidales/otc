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
STATUS_FILE=/var/lib/otc/update-status.json
LOG_FILE=/var/log/otc/update.log

mkdir -p "$(dirname "$STATUS_FILE")" "$(dirname "$LOG_FILE")" /etc/otc

# status <state> <message> - what the Settings screen reads back. Written
# to a file, not held in memory, because the service is restarted partway
# through and has to be able to report on an update it did not start.
status() {
    local state="$1" message="$2"
    printf '{"state":%s,"message":%s,"version":%s,"updated":%s}\n' \
        "\"$state\"" "\"${message//\"/\\\"}\"" "\"$(cat "$VERSION_FILE" 2>/dev/null || echo unknown)\"" \
        "\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"" > "$STATUS_FILE"
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

curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/VERSIONS" "$REPO_RAW/scripts/updates/VERSIONS" \
    || fail "could not fetch the release manifest"

installed="$(cat "$VERSION_FILE" 2>/dev/null || echo 0)"
echo "installed version: $installed"

# Releases are listed oldest first; anything numerically after what is
# installed is pending. Comments and blank lines are skipped.
pending=()
target=""
target_assets_sha=""
while IFS=$'\t' read -r version script_sha assets_sha summary; do
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

    # Most releases change no schema and carry no script at all.
    if [ "$sha" = "-" ]; then
        echo "release $version has no migration"
        echo "$version" > "$VERSION_FILE"
        continue
    fi

    curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/$version.sh" "$REPO_RAW/scripts/updates/$version.sh" \
        || fail "could not download release $version"

    # Checked before anything is executed as root. HTTPS already rules out
    # a MITM; this is what catches a truncated or corrupted download, and
    # a manifest that has drifted from the scripts it names.
    actual="$(sha256sum "$tmp/$version.sh" | awk '{print $1}')"
    if [ "$actual" != "$sha" ]; then
        fail "release $version failed its checksum (expected $sha, got $actual)"
    fi

    bash "$tmp/$version.sh" || fail "release $version failed to apply"

    # Written per release, not once at the end: an interrupted run then
    # resumes from the last one that actually completed.
    echo "$version" > "$VERSION_FILE"
    echo "release $version applied"
done

# Refreshing the code is common to every update, so it happens once here
# rather than in each release script.
# Pinned to the release's own tag rather than whatever main holds right
# now, so what gets built is exactly what this version is.
status running "Downloading the code for release $target"
curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/src.tar.gz" \
    "$REPO_GH/archive/refs/tags/v$target.tar.gz" \
    || fail "could not download the source for release $target"

mkdir -p "$tmp/src"
tar -xzf "$tmp/src.tar.gz" -C "$tmp/src" --strip-components=1 || fail "could not unpack the latest code"

status running "Staging the source"
mkdir -p "$SRC_DIR"
rsync -a --delete --exclude '.git' "$tmp/src/" "$SRC_DIR/" || fail "could not stage the new source"

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
if ! command -v protoc >/dev/null 2>&1 || ! protoc --version | grep -q " ${PROTOC_VERSION}$"; then
    curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/protoc.zip" \
        "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${PROTOC_ARCH}.zip" \
        || fail "could not download protoc ${PROTOC_VERSION}"
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

# The web app ships prebuilt, attached to the release. Devices have no
# Node - the bundle is built once, by whoever cuts the release, rather
# than on every Raspberry Pi in existence. The binary is still built here,
# which is what keeps any architecture supported without a cross-build.
if [ "$target_assets_sha" != "-" ] && [ -n "$target_assets_sha" ]; then
    status running "Installing the web app"
    if curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/web-dist.tar.gz" \
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
        echo "WARNING: release $target has no web assets attached, keeping the installed web app"
    fi
else
    echo "release $target ships no web assets, keeping the installed web app"
fi

status done "Updated to version $(cat "$VERSION_FILE")"
echo "=== update complete, restarting service ==="

# Last, and detached: this kills the process tree this script was started
# from, so nothing may follow it.
systemctl restart otc
