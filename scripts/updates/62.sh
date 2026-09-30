#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 62 (issue #156): no plaintext on the SD card through swap.
# The service's memory holds decrypted files and session keys; any page
# swapped to the card could keep them there. So:
# - Raspberry Pi OS 13's rpi-swap: zram only (compressed RAM). Its default,
#   zram+file, writes idle pages to /var/swap; with "zram" rpi-swap removes
#   that file on the next boot. Writeback stops now.
# - dphys-swapfile (older images, Makefile.pi's 4 GB swap): turned off and
#   its file removed.
# - otc.service: LimitMEMLOCK=infinity so the service can lock its memory
#   (it calls mlockall at start), and LimitCORE=0 - no core dumps.
# Idempotent. The swap change is complete after the next restart of the
# device; the service picks up the rest with this update.
set -euo pipefail

if [ -x /usr/lib/systemd/system-generators/rpi-swap-generator ] || [ -f /etc/rpi/swap.conf ]; then
    mkdir -p /etc/rpi/swap.conf.d
    printf '[Main]\nMechanism=zram\n' > /etc/rpi/swap.conf.d/90-otc-ram-only.conf
    systemctl stop rpi-zram-writeback.timer >/dev/null 2>&1 || true
fi

if command -v dphys-swapfile >/dev/null 2>&1; then
    dphys-swapfile swapoff >/dev/null 2>&1 || echo "WARNING: could not turn off the swap file (not enough free memory?) - it goes on the next restart"
    systemctl disable dphys-swapfile >/dev/null 2>&1 || true
    if ! grep -qs "$(awk -F= '/^CONF_SWAPFILE=/{print $2}' /etc/dphys-swapfile 2>/dev/null || echo /var/swap)" /proc/swaps; then
        dphys-swapfile uninstall >/dev/null 2>&1 || true
    fi
fi

mkdir -p /etc/systemd/system/otc.service.d
cat > /etc/systemd/system/otc.service.d/10-memory.conf <<'UNIT'
[Service]
LimitMEMLOCK=infinity
LimitCORE=0
UNIT
systemctl daemon-reload

echo "release 62 applied: swap stays in RAM, the service locks its memory"
