#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 108 (issue #192): folders kept out of Images.
#
# Two new tables, nothing altered - see db.sql's out_of_images_folders and
# skipped_analysis comments for the shape. Until they exist the device
# reads "no folder is kept out" (dao.GetOutOfImagesFolders).
#
# Idempotent: both are IF NOT EXISTS, so a device that already has them
# (a fresh install, where db.sql created them) does nothing here.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS out_of_images_folders (
  `path` VARCHAR(768) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  PRIMARY KEY (`path`)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS skipped_analysis (
  `hash` VARCHAR(64) NOT NULL,
  `skipped` DATETIME NOT NULL,
  PRIMARY KEY (`hash`)
) ENGINE=InnoDB;
SQL
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

echo "release 108 applied: out_of_images_folders, skipped_analysis"
