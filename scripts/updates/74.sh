#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 74 (issue #181): image tagging can be turned off in Settings.
# settings gains image_tagging_enabled (on by default) on the primary and
# every per-user database. Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS image_tagging_enabled TINYINT(1) NOT NULL DEFAULT 1;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 74 applied: image tagging switch"
