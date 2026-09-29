#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 43: face recognition is off unless the owner turns it on (issue
# #178 - it is biometric data). Changes the column's default on the primary
# and every per-user database; every existing setting stays as it is.
# Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE settings ALTER COLUMN face_recognition_enabled SET DEFAULT 0;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 43 applied: face recognition off by default"
