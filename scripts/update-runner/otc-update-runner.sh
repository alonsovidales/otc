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

status() {
    mkdir -p "$(dirname "$STATUS_FILE")"
    printf '{"state":%s,"message":%s,"version":%s,"updated":%s}\n' \
        "\"$1\"" "\"${2//\"/\\\"}\"" "\"$(cat /etc/otc/version 2>/dev/null || echo unknown)\"" \
        "\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"" > "$STATUS_FILE"
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

status running "Fetching the updater"
mkdir -p "$RUN_DIR" && chmod 700 "$RUN_DIR"
if ! curl -fsSL --retry 3 --retry-delay 2 -o "$RUN_DIR/update.sh" "$OTC_REPO_RAW/scripts/update.sh"; then
    status failed "could not download the updater from $OTC_REPO_RAW"
    exit 1
fi

exec /bin/bash "$RUN_DIR/update.sh"
