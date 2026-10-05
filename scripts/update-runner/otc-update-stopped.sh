#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# otc-update.service's ExecStopPost. Every run that ends on its own has
# written done, uptodate or failed by the time the unit stops, so a status
# still saying "running" here belongs to a run that was cut off: killed,
# out of memory, past the unit's start timeout, or stopped by a reboot.
# Left alone it would lock Settings' Update button for good, because the
# device refuses to start an update while one is "running".
set -uo pipefail

STATUS_FILE=/var/lib/otc/update-status.json

# The directory is the otc user's: read without following a symlink and
# without blocking on a FIFO put in the file's place.
dd if="$STATUS_FILE" iflag=nofollow,nonblock bs=4096 count=1 status=none 2>/dev/null \
    | grep -q '"state":"running"' || exit 0

# Unless an update.sh is still alive: one started by hand runs outside
# this unit and holds update.sh's lock (shared) while it lives, so that
# "running" is real. Any doubt leaves the file alone; the device makes the
# same check (updater.updateUnitGone) once nothing holds the lock. The -e
# because flock creates a lock file that isn't there.
RUN_LOCK=/run/otc-update.lock
if [ -e "$RUN_LOCK" ] && ! flock -n -x "$RUN_LOCK" true 2>/dev/null; then
    exit 0
fi

# Same write as otc-update-runner.sh's status(): O_EXCL temp file with its
# final mode, renamed into place.
status() {
    local tmp="$STATUS_FILE.tmp.$$.$RANDOM$RANDOM"
    printf '{"state":%s,"message":%s,"version":%s,"updated":%s}\n' \
        "\"$1\"" "\"${2//\"/\\\"}\"" "\"$(cat /etc/otc/version 2>/dev/null || echo unknown)\"" \
        "\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"" \
        | (umask 022; dd of="$tmp" conv=excl status=none 2>/dev/null) || { rm -f "$tmp"; return 1; }
    mv -Tf "$tmp" "$STATUS_FILE"
}

status failed "The update was interrupted - press Update to try again"
exit 0
