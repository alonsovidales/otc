#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The root half of Settings > Restart device. The device (otc.service,
# running unprivileged with NoNewPrivileges) can't reboot the machine, so
# when its owner asks it drops /var/lib/otc/reboot.request and this
# restarts the device. Same trigger-file shape as otc-update-runner.sh.
#
# Trust boundary: the request's content is never read - its existence is
# the whole message, and the only thing it can cause is a clean restart.
# A request left behind must never become a restart loop: it is removed
# before anything else, and one found in the first two minutes after boot
# is dropped (the device refuses those too, with words for the owner).
set -uo pipefail

REQUEST=/var/lib/otc/reboot.request
MIN_UPTIME=120

rm -f "$REQUEST"

up="$(cut -d. -f1 /proc/uptime 2>/dev/null)"
if [ -z "$up" ] || [ "$up" -lt "$MIN_UPTIME" ]; then
    echo "restart request dropped: up for only ${up:-?} s"
    exit 0
fi

echo "restarting the device, as its owner asked from Settings"
# On disk before the restart, and a moment for the device's answer to
# reach the app that asked.
sync
sleep 3
systemctl reboot
