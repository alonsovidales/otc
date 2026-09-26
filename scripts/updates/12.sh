#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 12 (issue #64): device errors as notifications.
#
# Three columns on notifications - see the column comments in db.sql.
# Idempotent: MariaDB's IF NOT EXISTS on each.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying schema to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS title VARCHAR(255) DEFAULT NULL;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS details TEXT DEFAULT NULL;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS occurrences INT NOT NULL DEFAULT 1;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 12 applied: notifications.title/details/occurrences"
