#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 35: a limit on the space friends' posts take (issue #153).
#
# The setting's column, 5 GB by default, on the primary database and every
# per-user database. Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS social_storage_limit_mb INT NOT NULL DEFAULT 5120;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 35 applied: social_storage_limit_mb"
