#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The root half of Tailscale Funnel (issue #80): tailscaled only runs on a
# device whose owner turned Funnel on. The device (otc.service, running
# unprivileged with NoNewPrivileges) can't start or stop a system service,
# so it drops /var/lib/otc/tailscale.request - "on" or "off" - and this
# does it. Same shape as otc-bridge-runner.sh.
#
# on:  enable and start tailscaled, and make otc its operator (which only
#      works with the daemon up), so the device drives the rest itself.
# off: stop and disable tailscaled - the device has already reset Funnel.
#      The node stays in the tailnet (its state is kept); it is just not
#      running until Funnel is turned on again.
#
# Trust boundary: the request's content is one of two words, nothing from
# it reaches a command line.
set -uo pipefail

REQUEST=/var/lib/otc/tailscale.request
STATUS_FILE=/var/lib/otc/tailscale-status.json

want="$(head -c 8 "$REQUEST" 2>/dev/null | tr -dc 'a-z')"
rm -f "$REQUEST"

# Temp file moved into place: the directory is the otc user's, and a plain
# "> $STATUS_FILE" as root would follow a symlink put there.
status() {
    local tmp
    tmp="$(mktemp "$STATUS_FILE.XXXXXX")" || return 1
    printf '{"state":"%s","message":"%s","updated":"%s"}\n' \
        "$1" "${2//\"/\\\"}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$tmp"
    chmod 644 "$tmp" && mv -Tf "$tmp" "$STATUS_FILE"
}

if ! command -v tailscale >/dev/null 2>&1; then
    status failed "Tailscale is not installed on this device"
    exit 1
fi

case "$want" in
on)
    if ! systemctl enable --now tailscaled >/dev/null 2>&1; then
        status failed "tailscaled could not be started"
        exit 1
    fi
    # The daemon takes a moment before it answers.
    for _ in $(seq 1 20); do
        tailscale status --json >/dev/null 2>&1 && break
        sleep 1
    done
    if ! tailscale set --operator=otc >/dev/null 2>&1; then
        status failed "tailscaled started, but the device could not be made its operator"
        exit 1
    fi
    status done "on"
    ;;
off)
    systemctl disable --now tailscaled >/dev/null 2>&1 || true
    status done "off"
    ;;
*)
    status failed "unknown request"
    exit 1
    ;;
esac
