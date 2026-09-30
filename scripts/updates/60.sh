#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 60 (issue #172): file paths compare exactly. The path columns
# used the database's accent- and case-insensitive collation, so deleting
# or sharing /Cafe/ also took /café/ and /CAFE/, and "photo.jpg" and
# "Photo.jpg" in one folder couldn't both be stored. Binary now, on the
# primary and every per-user database. Idempotent (MODIFY to the same
# definition is a no-op).
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE files MODIFY `path` VARCHAR(768) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;
ALTER TABLE file_versions MODIFY `path` VARCHAR(768) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;
ALTER TABLE upload_only_folders MODIFY `path` VARCHAR(768) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 60 applied: paths compare exactly"
