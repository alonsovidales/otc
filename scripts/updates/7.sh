#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 7: files.cloud_id - the photo library's own identifier for the
# asset a row came from (iOS's PHCloudIdentifier), so a phone can ask the
# device "do you already have these?" before downloading anything from
# iCloud just to hash it. See the column's comment in db.sql.
#
# Idempotent: MariaDB's IF NOT EXISTS on both the column and the index.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying schema to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE files ADD COLUMN IF NOT EXISTS cloud_id VARCHAR(255) NULL;
ALTER TABLE files ADD INDEX IF NOT EXISTS cloud_id (cloud_id);
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 7 applied: files.cloud_id"
