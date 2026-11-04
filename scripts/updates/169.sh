#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 169. Each part below is its own section, idempotent and safe on
# every older release.
set -uo pipefail

# --- dev-api: each rated link's visitors' own ratings ----------------------
#
# Collection Rating (release 163) kept one rating per photo and collection,
# shared by the owner and the visitors of every rated link of it: one client
# saw another's comments and the owner's own notes, and anyone the link
# reached could overwrite or clear the owner's. photo_rating_sources keeps
# each one's own rating - source '' the owner's, else the link's uuid - and
# photo_ratings stays what the owner sees, the latest change (see db.sql and
# dao/ratings.go). Only the table is made here: the device moves the ratings
# stored before into it at its next start (dao.ReconcileRatingSources), so a
# rating an older binary stores meanwhile is moved too. A database this
# can't change is reported and left: rating there answers an error until it
# has the table. Run on the primary database and on every per-user database
# (issue #82), each of which carries its own schema.

rating_sources_apply_to() {
    local db="$1"
    echo "applying to ${db}"
    if ! mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS photo_rating_sources (`group_id` VARCHAR(36) NOT NULL, `hash` VARCHAR(64) NOT NULL, `source` VARCHAR(64) NOT NULL, `stars` TINYINT NOT NULL DEFAULT 0, `comment` BLOB NULL, `link_comment` BLOB NULL, `updated` DATETIME(6) NOT NULL, PRIMARY KEY (`group_id`, `hash`, `source`), KEY (`hash`)) ENGINE=InnoDB;
SQL
    then
        echo "WARNING: release 169 could not add photo_rating_sources to ${db}; rating there fails until it has it"
    fi
}

rating_sources() {
    rating_sources_apply_to otc
    local db
    for db in $(mysql -N -B -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
        # The otc service can write this table: only a name the device
        # itself generates (dao checkGenerated, install.sh) reaches root's
        # mysql command line, where anything starting with - would be an
        # option.
        if ! [[ "$db" =~ ^otc_[0-9a-f]{32}$ ]]; then
            echo "skipping a users row whose db_name this device did not generate"
            continue
        fi
        if ! mysql -N -B -e "SHOW DATABASES LIKE '$db'" | grep -qxF -- "$db"; then
            echo "skipping $db: no such database"
            continue
        fi
        rating_sources_apply_to "$db"
    done
    echo "release 169: photo_rating_sources"
}

rating_sources
