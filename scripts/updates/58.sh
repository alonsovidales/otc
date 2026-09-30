#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 58 (issue #157): the storage is the service's alone. Blobs,
# thumbnails and friends' post media were written 0644 (world-readable;
# ciphertext for the blobs, not for friends' media), and the storage
# directories 0755. Files become 0600 and directories 0750, for every
# instance's storage paths ([otc] storage-path, unenc-storage-path).
# Idempotent.
set -euo pipefail

paths=$(awk -F= '/^[[:space:]]*(storage-path|unenc-storage-path)[[:space:]]*=/ {gsub(/[[:space:]]/, "", $2); print $2}' /etc/otc_*.ini 2>/dev/null | sort -u)

for p in $paths; do
    [ -d "$p" ] || continue
    case "$p" in /mnt/*|/var/lib/otc/*) ;; *) echo "skipping unexpected path $p"; continue ;; esac
    find "$p" -xdev -type f -perm /077 -exec chmod go-rwx {} +
    find "$p" -xdev -type d -perm /027 -exec chmod g-w,o-rwx {} +
    echo "tightened $p"
done

echo "release 58 applied: storage readable by the service only"
