#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 50: tailscaled runs only while Tailscale Funnel is on (issue #80).
# It used to run on every device from the moment Tailscale was installed.
# A device serving through Funnel keeps it; every other one stops and
# disables it - Settings starts it again (otc-tailscale-runner) when the
# owner turns Funnel on. update.sh installs that runner. Idempotent.
set -euo pipefail

if ! command -v tailscale >/dev/null 2>&1; then
    echo "tailscale is not installed here, nothing to do"
    exit 0
fi

if tailscale funnel status 2>/dev/null | grep -q ':8080'; then
    echo "Funnel is on: tailscaled stays"
else
    systemctl disable --now tailscaled >/dev/null 2>&1 || true
    echo "release 50 applied: tailscaled stopped until Funnel is turned on"
fi
