#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 158, as release 169 corrected it: update.sh runs this in place of
# updates/158.sh, which stays as it was signed (see README.md here).
#
# Image Tagging follows the device's memory until its user chooses (owner,
# 2026-11-01: "perhaps we can disable tagging to run in small devices"):
# off with less than 6 GB, where the RAM++ model took most of the memory,
# on otherwise - set by the device at every start
# (files_manager/tagging_default.go). settings.image_tagging_chosen marks a
# choice. The switch was on by default until then, so a library where it
# is off had it turned off by its user: that is marked as chosen, once,
# when the column is added - never again, or a default the device set
# would count as a choice.
#
# As it was, the column was added NOT NULL first and its absence was the
# mark: a run cut between adding it and marking the choices (a power cut)
# found it there the next time and skipped the marking, and the device then
# turned tagging back on for a user who had turned it off. The column is
# now added with no value, given one from the switch, and only then made
# NOT NULL: a column still NULL-able is a run to finish. The same as
# install.sh's schema block (updater/tagging_choice_test.go).
#
# Idempotent. Run as root by scripts/update.sh, on the primary database and
# on every per-user database (issue #82), each of which carries its own
# schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    if [ "$(mysql "$db" -N -B -e "SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'image_tagging_chosen'")" != NO ]; then
        mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS image_tagging_chosen TINYINT(1) NULL;
UPDATE settings SET image_tagging_chosen = (image_tagging_enabled = 0) WHERE image_tagging_chosen IS NULL;
ALTER TABLE settings MODIFY image_tagging_chosen TINYINT(1) NOT NULL DEFAULT 0;
SQL
    fi
}

apply_to otc

# The users list must be read: a query that fails is the database failing,
# not "no other users", and a device that has no users table at all (from
# before additional users) has nothing to loop over.
if ! users="$(mysql -N -B -e "SHOW TABLES LIKE 'users'" otc)"; then
    echo "release 158: could not read this device's tables" >&2
    exit 1
fi
if [ -n "$users" ]; then
    if ! dbs="$(mysql -N -B -e "SELECT db_name FROM users" otc)"; then
        echo "release 158: could not read this device's users" >&2
        exit 1
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
            echo "release 158: could not ask the database whether $db exists" >&2
            exit 1
        fi
        if ! grep -qxF -- "$db" <<<"$found"; then
            echo "skipping $db: no such database"
            continue
        fi
        apply_to "$db"
    done
fi

echo "release 158 applied: settings.image_tagging_chosen"
