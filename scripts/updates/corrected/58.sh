#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 58, as release 169 corrected it: update.sh runs this in place of
# updates/58.sh, which stays as it was signed (see README.md here).
#
# Release 58 (issue #157): the storage is the service's alone. Blobs,
# thumbnails and friends' post media were written 0644 (world-readable;
# ciphertext for the blobs, not for friends' media), and the storage
# directories 0755. Files become 0600 and directories 0750, for every
# instance's storage paths ([otc] storage-path, unenc-storage-path).
#
# As it was, root ran the chmods over a tree the otc user owns: a file
# swapped for a symlink after find listed it made root take the group and
# other permissions away from whatever the link pointed at, anywhere on
# the system. They now run as the otc user, on what it owns - everything
# but MariaDB's own directory and lost+found, which are left as they are.
#
# Idempotent.
set -euo pipefail

paths=$(awk -F= '/^[[:space:]]*(storage-path|unenc-storage-path)[[:space:]]*=/ {gsub(/[[:space:]]/, "", $2); print $2}' /etc/otc_*.ini 2>/dev/null | sort -u)

if ! id otc >/dev/null 2>&1; then
    echo "release 58: no otc user, so no storage of its to tighten"
    exit 0
fi

for p in $paths; do
    p="${p%/}"
    [ -d "$p" ] || continue
    case "$p" in /mnt/*|/var/lib/otc/*) ;; *) echo "skipping unexpected path $p"; continue ;; esac
    runuser -u otc -- find "$p" -xdev \( -path "$p/mysql" -o -path "$p/lost+found" \) -prune \
        -o -user otc -type f -perm /077 -exec chmod go-rwx {} + \
        || echo "WARNING: not every file under $p could be made the service's alone"
    runuser -u otc -- find "$p" -xdev \( -path "$p/mysql" -o -path "$p/lost+found" \) -prune \
        -o -user otc -type d -perm /027 -exec chmod g-w,o-rwx {} + \
        || echo "WARNING: not every directory under $p could be made the service's alone"
    echo "tightened $p"
done

echo "release 58 applied: storage readable by the service only"
