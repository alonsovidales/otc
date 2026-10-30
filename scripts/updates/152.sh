#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 152: dates taken - a photo or video whose own metadata says it was
# taken well before the date its file carries (a library copied to a drive
# and synced from there years later) is shown at the date it was taken.
#
# Two settings columns for the one-off pass that gives what each device
# already holds its dates taken, in the background after the first sign-in
# (files_manager/date_taken.go): how far it has read, and whether it is done
# - see db.sql's settings.dates_taken_cursor and dates_taken_backfilled. No
# date is changed here: the device does it, from metadata it decrypts.
#
# Idempotent: ADD COLUMN IF NOT EXISTS.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS dates_taken_cursor VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE settings ADD COLUMN IF NOT EXISTS dates_taken_backfilled TINYINT(1) NOT NULL DEFAULT 0;
SQL
}

apply_to otc

for db in $(mysql -N -B -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    # The otc service can write this table: only a name the device itself
    # generates (dao checkGenerated, install.sh) reaches root's mysql
    # command line, where anything starting with - would be an option.
    if ! [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]]; then
        echo "skipping a users row whose db_name this device did not generate"
        continue
    fi
    if ! mysql -N -B -e "SHOW DATABASES LIKE '$db'" | grep -qxF -- "$db"; then
        echo "skipping $db: no such database"
        continue
    fi
    apply_to "$db"
done

echo "release 152 applied: settings.dates_taken_cursor, settings.dates_taken_backfilled"
