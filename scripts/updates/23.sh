#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 23: WiFi power saving off.
#
# The Raspberry Pi's WiFi driver (brcmfmac) starts with power saving on,
# a well-known cause of the WiFi dropping at random and not coming back
# until a restart - Cala went unreachable that way. A server has no use for
# it. Made permanent through NetworkManager (every connection's default)
# and applied to the running interface now, without restarting the
# network under the running update. Idempotent.
set -euo pipefail

CONF=/etc/NetworkManager/conf.d/otc-wifi-powersave.conf
if [ -d /etc/NetworkManager ]; then
    mkdir -p /etc/NetworkManager/conf.d
    cat > "$CONF" <<'CONF'
# Off The Cloud: WiFi power saving off (2 = disable) - it makes the
# Raspberry Pi's WiFi drop at random. Written by release 23 / install.sh.
[connection]
wifi.powersave = 2
CONF
    nmcli general reload conf >/dev/null 2>&1 || true
fi

if command -v iw >/dev/null 2>&1; then
    for dev in $(iw dev 2>/dev/null | awk '$1 == "Interface" {print $2}'); do
        iw dev "$dev" set power_save off 2>/dev/null || true
    done
fi

echo "release 23 applied: WiFi power saving off"
