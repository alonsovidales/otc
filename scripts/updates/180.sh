#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 180.
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

# --- dev-api: Text in photos and Pets ---------------------------------------
#
# The device reads the text in each photo (photo_text, sealed with the
# user's key) and finds the dogs and cats in them, each pet a person in
# People whose faces are of their own kind (faces.kind) - with the photos
# looked at for pets (pet_scans), and both switches, on by default (see
# db.sql). Only the columns and tables are made here; the device reads the
# photos it holds in the background once the models it downloads by
# itself are there. The same statements as scripts/install.sh's (updater/
# release180_test.go). Without them the device reads no text and finds no
# pets, and goes on as before. A failure stops the update here: it runs
# again with the next one. Run on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.

dev_text_and_pets_to() {
    local db="$1"
    if ! mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS text_in_photos_enabled TINYINT(1) NOT NULL DEFAULT 1;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS pets_enabled TINYINT(1) NOT NULL DEFAULT 1;
ALTER TABLE faces ADD COLUMN IF NOT EXISTS `kind` TINYINT NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS photo_text (`hash` VARCHAR(64) NOT NULL, `model` VARCHAR(64) NOT NULL, `text` MEDIUMBLOB NULL, `made` DATETIME NOT NULL, PRIMARY KEY (`hash`)) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS pet_scans (`hash` VARCHAR(64) NOT NULL, `model` VARCHAR(64) NOT NULL, `made` DATETIME NOT NULL, PRIMARY KEY (`hash`)) ENGINE=InnoDB;
SQL
    then
        echo "release 180: could not add Text in photos and Pets to ${db}" >&2
        return 1
    fi
}

dev_text_and_pets() {
    local tables dbs db found
    dev_text_and_pets_to otc || return 1
    if ! tables="$(mysql -N -B -e "SHOW TABLES LIKE 'users'" otc)"; then
        echo "release 180: could not read this device's tables" >&2
        return 1
    fi
    # A device from before additional users has no users table at all.
    [ -n "$tables" ] || return 0
    if ! dbs="$(mysql -N -B -e "SELECT db_name FROM users" otc)"; then
        echo "release 180: could not read this device's users" >&2
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
            echo "release 180: could not ask the database whether $db exists" >&2
            return 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database"
            continue
        fi
        dev_text_and_pets_to "$db" || return 1
    done
}

dev_text_and_pets || exit 1
echo "release 180: Text in photos and Pets in every database"
