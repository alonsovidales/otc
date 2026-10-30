#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 152.
#
# Smart search on by default (settings.smart_search_enabled defaults to 1,
# see db.sql): every library is turned on once, the ones release 151 left
# off included, and the column then defaults to on, so a library created
# later starts with it on too. Its model isn't fetched here: the device
# downloads it itself, in the background at a capped speed, once Smart
# search is on and the model isn't there (files_manager/smart_model.go), and
# describes the photos after the next sign-in.
#
# Dates taken: a photo or video whose own metadata says it was taken well
# before the date its file carries (a library copied to a drive and synced
# from there years later) is shown at the date it was taken. Two settings
# columns for the one-off pass that gives what each device already holds its
# dates taken, in the background after the first sign-in
# (files_manager/date_taken.go): how far it has read, and whether it is done
# - see db.sql's settings.dates_taken_cursor and dates_taken_backfilled. No
# date is changed here: the device does it, from metadata it decrypts.
#
# Idempotent: for Smart search the column's default is the mark - a database
# whose column already defaults to on was turned on before, and a library its
# user turned off since is left alone; a database without the column (151's
# script not applied) gets it, on. The dates columns are ADD COLUMN IF NOT
# EXISTS.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

smart_search_on() {
    local db="$1" default
    mysql "$db" -e 'ALTER TABLE settings ADD COLUMN IF NOT EXISTS smart_search_enabled TINYINT(1) NOT NULL DEFAULT 1'
    default="$(mysql "$db" -N -B -e "SELECT COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'smart_search_enabled'")"
    if [ "$default" = 0 ]; then
        # On first, then the default: a run cut between the two does the
        # first again.
        mysql "$db" <<'SQL'
UPDATE settings SET smart_search_enabled = 1;
ALTER TABLE settings ALTER COLUMN smart_search_enabled SET DEFAULT 1;
SQL
        echo "${db}: Smart search turned on"
    else
        echo "${db}: Smart search already on by default"
    fi
}

dates_taken_columns() {
    local db="$1"
    mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS dates_taken_cursor VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE settings ADD COLUMN IF NOT EXISTS dates_taken_backfilled TINYINT(1) NOT NULL DEFAULT 0;
SQL
}

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    smart_search_on "$db"
    dates_taken_columns "$db"
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

echo "release 152 applied: Smart search on by default; settings.dates_taken_cursor, settings.dates_taken_backfilled"
