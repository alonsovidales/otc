#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 158.
#
# Image Tagging follows the device's memory until its user chooses (owner,
# 2026-11-01: "perhaps we can disable tagging to run in small devices"):
# off with less than 6 GB, where the RAM++ model took most of the memory,
# on otherwise - set by the device at every start
# (files_manager/tagging_default.go). settings.image_tagging_chosen marks a
# choice. The switch was on by default until now, so a library where it is
# off had it turned off by its user: that is marked as chosen, once, when
# the column is added - never again, or a default the device set would
# count as a choice.
#
# Idempotent: the column is added, and the choice marked, only when it
# isn't there.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    if [ -z "$(mysql "$db" -N -B -e "SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'image_tagging_chosen'")" ]; then
        mysql "$db" <<'SQL'
ALTER TABLE settings ADD COLUMN IF NOT EXISTS image_tagging_chosen TINYINT(1) NOT NULL DEFAULT 0;
UPDATE settings SET image_tagging_chosen = 1 WHERE image_tagging_enabled = 0;
SQL
    fi
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

echo "release 158 applied: settings.image_tagging_chosen"
