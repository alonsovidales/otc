#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 22 (issue #141): storage that came from an older installation.
#
# A device recovered from its disks (the setup wizard's "Recover") keeps
# the files the previous installation wrote - and on an installation where
# the service ran as `pi`, they are owned by `pi`, while the service now runs
# as `otc`. It can read them (downloads work), but every write to an
# existing one fails with "permission denied": restoring missing content,
# regenerating thumbnails, deleting - and additional users' storage
# directories are pi-only (0750), so their processes (also `otc`) can't
# reach their files at all. Found on Cala: 46,980 of 47,051 blobs.
#
# Hands everything under the storage path except the database's own
# directory to otc:otc. Idempotent: files already owned by otc are left
# alone, and a device installed from scratch has nothing to change.
set -euo pipefail

STORAGE=""
for f in /etc/otc_*.ini; do
    [ -f "$f" ] || continue
    STORAGE=$(sed -n 's/^storage-path=//p' "$f" | head -1)
    [ -n "$STORAGE" ] && break
done
STORAGE="${STORAGE%/}"
if [ -z "$STORAGE" ] || [ ! -d "$STORAGE" ]; then
    echo "release 22: no storage path found - nothing to do"
    exit 0
fi
if ! id otc >/dev/null 2>&1; then
    echo "release 22: no otc user - nothing to do"
    exit 0
fi

# -xdev: stay on this filesystem; mysql and lost+found are not ours.
n=$(find "$STORAGE" -xdev \( -path "$STORAGE/mysql" -o -path "$STORAGE/lost+found" \) -prune -o ! -user otc -print | wc -l)
if [ "$n" -gt 0 ]; then
    find "$STORAGE" -xdev \( -path "$STORAGE/mysql" -o -path "$STORAGE/lost+found" \) -prune -o ! -user otc -exec chown -h otc:otc {} +
fi
# Additional users' directories: readable by their own process (otc).
for d in "$STORAGE"/user_*; do
    [ -d "$d" ] && chmod u+rwx "$d"
done

echo "release 22 applied: $n file(s) under $STORAGE handed to otc"
