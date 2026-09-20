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
REPO_RAW="${OTC_REPO_RAW:-https://raw.githubusercontent.com/alonsovidales/otc/main}"
SRC_TARBALL="${OTC_SRC_TARBALL:-https://github.com/alonsovidales/otc/archive/refs/heads/main.tar.gz}"
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
while IFS=$'\t' read -r version sha description; do
    case "$version" in ''|\#*) continue ;; esac
    if [ "$version" -gt "$installed" ] 2>/dev/null; then
        pending+=("$version"$'\t'"$sha")
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
status running "Downloading the latest code"
curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/src.tar.gz" "$SRC_TARBALL" \
    || fail "could not download the latest code"

mkdir -p "$tmp/src"
tar -xzf "$tmp/src.tar.gz" -C "$tmp/src" --strip-components=1 || fail "could not unpack the latest code"

status running "Building"
mkdir -p "$SRC_DIR"
rsync -a --delete --exclude '.git' "$tmp/src/" "$SRC_DIR/" || fail "could not stage the new source"

cd "$SRC_DIR" || fail "no source directory at $SRC_DIR"
export PATH="$PATH:/usr/local/go/bin"
# Built into a temporary name and moved into place only on success: a
# failed build must leave the running binary alone rather than truncating
# the one the service needs to start again.
CGO_ENABLED=1 go build -o "$tmp/otc" ./bin/otc.go || fail "the build failed - see $LOG_FILE"

status running "Restarting"
install -m 0755 "$tmp/otc" /usr/bin/otc || fail "could not install the new binary"

# The web app is served from disk, so it has to be rebuilt too when it
# changed. Skipped rather than fatal if npm isn't on this device.
if command -v npm >/dev/null 2>&1 && [ -d "$SRC_DIR/web" ]; then
    (cd "$SRC_DIR/web" && npm ci --silent && npm run build --silent) \
        && rsync -a "$SRC_DIR/web/dist/" /var/www/ \
        || echo "WARNING: the web app could not be rebuilt, keeping the one already installed"
fi

status done "Updated to version $(cat "$VERSION_FILE")"
echo "=== update complete, restarting service ==="

# Last, and detached: this kills the process tree this script was started
# from, so nothing may follow it.
systemctl restart otc
