#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 179.
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

# --- dev-api: people added to a photo or video by hand ---------------------
#
# The viewers' Info adds a person to a photo face recognition didn't find
# them in, or to a video (AddToPerson). people_by_hand keeps one row per
# person and content, apart from the faces found, which drive matching and
# covers (see db.sql and dao/people_by_hand.go). Only the table is made
# here. The same statement as scripts/install.sh's (updater/
# release179_test.go). Without it the device lists no one added by hand
# and refuses to add anyone, and goes on as before. A failure stops the
# update here: it runs again with the next one. Run on the primary database
# and on every per-user database (issue #82), each of which carries its own
# schema.

dev_people_by_hand_to() {
    local db="$1"
    if ! mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS people_by_hand (`hash` VARCHAR(64) NOT NULL, `person_id` VARCHAR(36) NOT NULL, `created` DATETIME NOT NULL, PRIMARY KEY (`person_id`, `hash`), KEY (`hash`)) ENGINE=InnoDB;
SQL
    then
        echo "release 179: could not add people_by_hand to ${db}" >&2
        return 1
    fi
}

dev_people_by_hand() {
    local tables dbs db found
    dev_people_by_hand_to otc || return 1
    if ! tables="$(mysql -N -B -e "SHOW TABLES LIKE 'users'" otc)"; then
        echo "release 179: could not read this device's tables" >&2
        return 1
    fi
    # A device from before additional users has no users table at all.
    [ -n "$tables" ] || return 0
    if ! dbs="$(mysql -N -B -e "SELECT db_name FROM users" otc)"; then
        echo "release 179: could not read this device's users" >&2
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
            echo "release 179: could not ask the database whether $db exists" >&2
            return 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database"
            continue
        fi
        dev_people_by_hand_to "$db" || return 1
    done
}

dev_people_by_hand || exit 1
echo "release 179: people_by_hand in every database"
