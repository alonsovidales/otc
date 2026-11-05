#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 170.
#
# Each change below is a section of its own, headed "# --- <area>: <what>
# ---", and each one is idempotent and safe on every older release (this
# script runs on a device after every release before it, in order). set -e
# is off on purpose: each section says what its failure means - exit 1
# stops the update here, to be run again by the next one; a warning lets
# the rest go on.
#
# Run as root by scripts/update.sh.
set -uo pipefail

# --- dev-places: Places on by default -------------------------------------
#
# Places, the map in Images, was off until its user turned it on (release
# 148). It is now on unless they turn it off: every library is turned on
# once, and the column then defaults to on, so a library created later
# starts with it on too (db.sql's settings.places_enabled). Nothing is read
# here: the device reads where the photos it holds were taken itself, in
# the background after the next sign-in (files_manager/location_backfill.go).
#
# The column's default is the mark, as release 152's for Smart search: a
# database whose column already defaults to on was turned on before, and a
# library its user turned off since is left alone. The update comes before
# the default, so a run cut between the two does it again. The same
# statements as scripts/install.sh's (updater/release170_test.go). A
# failure stops the update here: it runs again with the next one.

dev_places_on_to() {
    local db="$1"
    if ! mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS places_enabled TINYINT(1) NOT NULL DEFAULT 1;
UPDATE settings SET places_enabled = 1 WHERE (SELECT COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'places_enabled') = '0';
ALTER TABLE settings ALTER COLUMN places_enabled SET DEFAULT 1;
SQL
    then
        echo "release 170: could not turn Places on in ${db}" >&2
        return 1
    fi
}

dev_places_on() {
    local tables dbs db found
    dev_places_on_to otc || return 1
    if ! tables="$(mysql -N -B -e "SHOW TABLES LIKE 'users'" otc)"; then
        echo "release 170: could not read this device's tables" >&2
        return 1
    fi
    # A device from before additional users has no users table at all.
    [ -n "$tables" ] || return 0
    if ! dbs="$(mysql -N -B -e "SELECT db_name FROM users" otc)"; then
        echo "release 170: could not read this device's users" >&2
        return 1
    fi
    for db in $dbs; do
        # The otc service can write this table: only a name the device
        # itself generates (dao checkGenerated, install.sh) reaches root's
        # mysql command line, where anything starting with - would be an
        # option.
        if ! [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]]; then
            echo "skipping a users row whose db_name this device did not generate"
            continue
        fi
        if ! found="$(mysql -N -B -e "SHOW DATABASES LIKE '$db'")"; then
            echo "release 170: could not ask the database whether $db exists" >&2
            return 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database"
            continue
        fi
        dev_places_on_to "$db" || return 1
    done
}

dev_places_on || exit 1
echo "release 170: Places is on by default in every database"
