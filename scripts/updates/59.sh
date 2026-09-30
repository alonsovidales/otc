#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 59 (issue #157): SSH never accepts passwords. The image's
# console account (otc-debug) has a published password; SSH is off on
# the image, but turned on the usual way it would let anyone on the LAN
# sign in with it. Keys only, every account - the console still works.
# Read first (01-): sshd keeps the first value it sees. Idempotent.
set -euo pipefail

mkdir -p /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/01-otc-keys-only.conf <<'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
EOF
chmod 644 /etc/ssh/sshd_config.d/01-otc-keys-only.conf

# Only if SSH is running (and its config is valid): a device with SSH off
# picks this up whenever it's turned on.
if systemctl is-active --quiet ssh 2>/dev/null; then
    if sshd -t 2>/dev/null; then
        systemctl reload ssh
    else
        echo "WARNING: sshd -t failed, not reloading ssh"
    fi
fi

echo "release 59 applied: SSH accepts keys only"
