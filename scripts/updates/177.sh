#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 177.
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

# --- dev-api: a compressed file's original, its bytes deleted ---------------
#
# Compress (Settings › Largest files, Images) re-encodes a photo or video
# smaller in place of every copy, saved as an edit is (edited_content, 174),
# and then deletes the original's bytes to free the space, keeping its hash:
# the phones and computers that hold it are answered with the compressed
# file and never send it again, and Free up space never takes their copy
# (see db.sql and files_manager/compress_job.go). original_removed says the
# original is gone. Only the column is added here. The same statement as
# scripts/install.sh's (updater/release177_test.go). Without it the device
# offers nothing to compress and goes on as before. A failure stops the
# update here: it runs again with the next one. Run on the primary database
# and on every per-user database (issue #82), each of which carries its own
# schema.

dev_compress_to() {
    local db="$1"
    if ! mysql "$db" <<'SQL'
ALTER TABLE edited_content ADD COLUMN IF NOT EXISTS `original_removed` TINYINT(1) NOT NULL DEFAULT 0;
SQL
    then
        echo "release 177: could not add original_removed to ${db}'s edited_content" >&2
        return 1
    fi
}

dev_compress() {
    local tables dbs db found
    dev_compress_to otc || return 1
    if ! tables="$(mysql -N -B -e "SHOW TABLES LIKE 'users'" otc)"; then
        echo "release 177: could not read this device's tables" >&2
        return 1
    fi
    # A device from before additional users has no users table at all.
    [ -n "$tables" ] || return 0
    if ! dbs="$(mysql -N -B -e "SELECT db_name FROM users" otc)"; then
        echo "release 177: could not read this device's users" >&2
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
            echo "release 177: could not ask the database whether $db exists" >&2
            return 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database"
            continue
        fi
        dev_compress_to "$db" || return 1
    done
}

dev_compress || exit 1
echo "release 177: original_removed in every database"
