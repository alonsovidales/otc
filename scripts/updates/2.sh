#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 2 (issue #80): let the otc user drive Tailscale without root.
#
# otc.service runs with NoNewPrivileges=true, so anything the device
# itself shells out to can never gain privilege - sudo fails there with
# "the no new privileges flag is set" however sudoers is written. An
# operator is Tailscale's own answer: it hands one local user permission
# to drive the daemon directly, which is also the better outcome, since
# the device then needs no path to root at all.
#
# Idempotent: setting the same operator twice is a no-op, and a device
# without Tailscale installed simply has nothing to do.
set -euo pipefail

if ! command -v tailscale >/dev/null 2>&1; then
    echo "tailscale is not installed here, nothing to do"
    exit 0
fi

tailscale set --operator=otc
echo "release 2 applied: otc is now the Tailscale operator"
