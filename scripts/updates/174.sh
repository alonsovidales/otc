#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 174.
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

# --- dev-api: photo edits keep their originals -----------------------------
#
# The viewers rotate and crop a photo (EditImage). The edit is new content
# with a hash of its own; edited_content names the photo it was made from,
# whose content the device then keeps - hidden, with no path - so the edit
# can be reverted and a phone's sync still finds the photo it sends already
# there (see db.sql and files_manager/edit_image.go). Only the table is made
# here. The same statement as scripts/install.sh's (updater/
# release174_test.go). Without it the device refuses edits ("not_editable")
# and goes on as before. A failure stops the update here: it runs again with
# the next one. Run on the primary database and on every per-user database
# (issue #82), each of which carries its own schema.

dev_edits_to() {
    local db="$1"
    if ! mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS edited_content (`hash` VARCHAR(64) NOT NULL, `original_hash` VARCHAR(64) NOT NULL, `original_mime` VARCHAR(150) NOT NULL, `original_size` BIGINT NOT NULL, `original_ext` VARCHAR(16) NOT NULL DEFAULT '', `edited` DATETIME NOT NULL, PRIMARY KEY (`hash`), KEY (`original_hash`)) ENGINE=InnoDB;
SQL
    then
        echo "release 174: could not add edited_content to ${db}" >&2
        return 1
    fi
}

dev_edits() {
    local tables dbs db found
    dev_edits_to otc || return 1
    if ! tables="$(mysql -N -B -e "SHOW TABLES LIKE 'users'" otc)"; then
        echo "release 174: could not read this device's tables" >&2
        return 1
    fi
    # A device from before additional users has no users table at all.
    [ -n "$tables" ] || return 0
    if ! dbs="$(mysql -N -B -e "SELECT db_name FROM users" otc)"; then
        echo "release 174: could not read this device's users" >&2
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
            echo "release 174: could not ask the database whether $db exists" >&2
            return 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database"
            continue
        fi
        dev_edits_to "$db" || return 1
    done
}

dev_edits || exit 1
echo "release 174: edited_content in every database"
