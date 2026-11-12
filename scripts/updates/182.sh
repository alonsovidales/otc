#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 182.
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

# --- dev-pets: Pets off by default --------------------------------------------
#
# Pets, which files the dogs and cats in the photos in People, was on in
# every library from release 180, and People filled with pets for users
# who don't care about them. It is now off unless its user turns it on: a
# library where it has found no pet (no pet's face, faces.kind) is turned
# off once - one updated from before 180 too, whose column 180.sh has just
# added on - and the column then defaults to off, so a library made later
# starts with it off (db.sql's settings.pets_enabled). A library where it
# found pets keeps it on: its user has seen them in People, and turns it
# off in Settings. Nothing is deleted.
#
# The column's default is the mark, as release 170's for Places: a
# database whose column already defaults to off was done before, and a
# library its user turned on since is left alone. The update comes before
# the default, so a run cut between the two does it again. The same
# statements as scripts/install.sh's (updater/release182_test.go). A
# failure stops the update here: it runs again with the next one.

dev_pets_off_to() {
    local db="$1"
    if ! mysql "$db" <<'SQL'
UPDATE settings SET pets_enabled = 0 WHERE (SELECT COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'pets_enabled') = '1' AND NOT EXISTS (SELECT 1 FROM faces WHERE kind <> 0);
ALTER TABLE settings ALTER COLUMN pets_enabled SET DEFAULT 0;
SQL
    then
        echo "release 182: could not turn Pets off in ${db}" >&2
        return 1
    fi
}

dev_pets_off() {
    local tables dbs db found
    dev_pets_off_to otc || return 1
    if ! tables="$(mysql -N -B -e "SHOW TABLES LIKE 'users'" otc)"; then
        echo "release 182: could not read this device's tables" >&2
        return 1
    fi
    # A device from before additional users has no users table at all.
    [ -n "$tables" ] || return 0
    if ! dbs="$(mysql -N -B -e "SELECT db_name FROM users" otc)"; then
        echo "release 182: could not read this device's users" >&2
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
            echo "release 182: could not ask the database whether $db exists" >&2
            return 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database"
            continue
        fi
        dev_pets_off_to "$db" || return 1
    done
}

dev_pets_off || exit 1
echo "release 182: Pets is off by default in every database, and off where it found no pet"
