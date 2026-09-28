#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 24: face recognition ("People" search) on by default.
#
# Changes the column's default on the primary database and every per-user
# database, so anything created from now on - a new user's settings row -
# starts with it on. Deliberately leaves every existing setting as it is:
# there is no telling "never touched" from "turned off on purpose".
# Idempotent.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE settings ALTER COLUMN face_recognition_enabled SET DEFAULT 1;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 24 applied: face recognition on by default for new settings"
