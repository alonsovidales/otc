#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The root half of Settings > Shut down device (issue #202). The device
# (otc.service, running unprivileged with NoNewPrivileges) can't power the
# machine off, so when its owner asks it drops /var/lib/otc/poweroff.request
# and this powers the device off cleanly - `systemctl poweroff` stops the
# service and MariaDB and unmounts the disks first - so it can be unplugged
# without risking the SD card. It stays off until its power is unplugged
# and plugged in again. Same trigger-file shape as otc-reboot-runner.sh.
#
# Trust boundary: the request's content is never read - its existence is
# the whole message, and the only thing it can cause is a clean shutdown.
# A request left behind must never switch the device off as it starts (it
# could then never stay on): it is removed before anything else, and one
# found in the first two minutes after boot is dropped (the device refuses
# those too, with words for the owner). Nor in the middle of an update or a
# Wi-Fi change, which the device refuses as well: one that slipped past it
# is dropped here, and the Wi-Fi lock is held until the machine goes down,
# so no change starts meanwhile.
#
# --dry-run, by hand as root: the same checks, saying what it would do,
# without removing the request, taking the lock or powering anything off.
set -uo pipefail

REQUEST=/var/lib/otc/poweroff.request
UPDATE_REQUEST=/var/lib/otc/update.request
UPDATE_LOCK=/run/otc-update.lock
WIFI_LOCK=/run/otc-wifi.lock
MIN_UPTIME=120

dry=0
[ "${1:-}" = "--dry-run" ] && dry=1

log() { echo "[otc-poweroff] $*"; }
drop() {
    if [ "$dry" = 1 ]; then log "dry run: would drop the request: $*"; else log "request dropped: $*"; fi
    exit 0
}

[ "$dry" = 1 ] || rm -f "$REQUEST"

up="$(cut -d. -f1 /proc/uptime 2>/dev/null)"
if [ -z "$up" ] || [ "$up" -lt "$MIN_UPTIME" ]; then
    drop "up for only ${up:-?} s"
fi

# An update asked for or running: its request still waiting for the update
# runner (lstat only: never opened), the runner's unit at work (downloading,
# before update.sh), or update.sh holding its lock - shared, so an exclusive
# one that can't be had means it runs; released at once, as update.sh waits
# only 10 s for its own. Switching off then could leave it half done.
if [ -e "$UPDATE_REQUEST" ] || [ -L "$UPDATE_REQUEST" ]; then
    drop "an update was asked for"
fi
case "$(systemctl is-active otc-update.service 2>/dev/null)" in
    active|activating|reloading) drop "an update is running" ;;
esac
if [ -e "$UPDATE_LOCK" ] && ! flock -n -x "$UPDATE_LOCK" true 2>/dev/null; then
    drop "an update is running"
fi

# Settings > Wi-Fi changes the network as root (otc-wifi-join-runner),
# holding this lock for the whole change: switching off in the middle of
# one could leave the device on a network it can't join. Held (fd 8) until
# the machine goes down; in root's /run, so nothing the otc user planted is
# opened.
if [ "$dry" = 1 ]; then
    if [ -e "$WIFI_LOCK" ] && ! flock -n "$WIFI_LOCK" true 2>/dev/null; then
        drop "the Wi-Fi is being changed from Settings"
    fi
    log "dry run: would power the device off now"
    exit 0
fi
: >> "$WIFI_LOCK"
exec 8<"$WIFI_LOCK"
flock -n 8 || drop "the Wi-Fi is being changed from Settings"

log "powering the device off, as its owner asked from Settings"
# On disk before the shutdown, and a moment for the device's answer to
# reach the app that asked.
sync
sleep 3
systemctl poweroff
