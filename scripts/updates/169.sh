#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 169. Each part below is a section of its own; every one is
# idempotent and safe on any older release.
#
# Run as root by scripts/update.sh.
set -uo pipefail

# --- dev-runtime: raid-watch.service started again after any failure ---
# raid_watch.py drives the status lights, takes down and brings back the
# storage (issue #207) and repairs the mirror. Its unit restarted it 100 ms
# after a failure, and systemd's default start limit (five starts in ten
# seconds) then left it off until a reboot - no lights, nobody watching the
# storage. Units installed before this release get the two lines
# scripts/install.sh now writes: started again 5 s after a failure, with no
# start limit (updater's TestRaidWatchUnitIsTheSameInInstallAndRelease169).
# The script itself - which no longer ends on an unexpected error, nor
# waits long on a disk that stopped answering - is reinstalled by update.sh
# at every update. Nothing where there is no such unit (a container, a
# demo device); a line already there is left as it is.
dev_runtime_raid_watch_restart() {
    local unit="$1" tmp limit=1 delay=1
    grep -q '^StartLimitIntervalSec=' "$unit" && limit=0
    grep -q '^RestartSec=' "$unit" && delay=0
    [ "$limit$delay" != 00 ] || return 0
    tmp="$unit.otc.$$"
    if awk -v limit="$limit" -v delay="$delay" '
            { print }
            $0 == "[Unit]" && limit == 1 { print "StartLimitIntervalSec=0"; limit = 0 }
            $0 == "[Service]" && delay == 1 { print "RestartSec=5"; delay = 0 }' "$unit" > "$tmp" \
        && chmod 0644 "$tmp" && mv -f "$tmp" "$unit"; then
        return 0
    fi
    rm -f "$tmp"
    return 1
}

dev_runtime_raid_watch() {
    local unit=/etc/systemd/system/raid-watch.service before
    [ -f "$unit" ] || return 0
    before="$(cksum < "$unit")"
    if ! dev_runtime_raid_watch_restart "$unit"; then
        echo "WARNING: release 169: could not update raid-watch.service's restart settings" >&2
        return 0
    fi
    [ "$(cksum < "$unit")" != "$before" ] || return 0
    systemctl daemon-reload || true
    echo "release 169: raid-watch.service is started again 5 s after a failure, with no start limit"
}
dev_runtime_raid_watch
