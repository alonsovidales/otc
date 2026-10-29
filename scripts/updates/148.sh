#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 148: Places, the map in Images.
#
# One new table and two settings columns - see db.sql's photo_locations,
# settings.locations_backfilled and settings.places_enabled for the shape.
# Places is off on every device after this (places_enabled 0): only once a
# user turns it on does the device fill the table, as it processes photos
# and videos and once in the background for what it held before, then set
# locations_backfilled. Until they exist the device reads Places as off.
#
# Idempotent: IF NOT EXISTS on each, so a device that already has them (a
# fresh install, where db.sql created them) does nothing here.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying to ${db}"
    mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS photo_locations (
  `hash` VARCHAR(64) NOT NULL,
  `lat` DOUBLE NULL,
  `lon` DOUBLE NULL,
  PRIMARY KEY (`hash`)
) ENGINE=InnoDB;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS locations_backfilled TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE settings ADD COLUMN IF NOT EXISTS places_enabled TINYINT(1) NOT NULL DEFAULT 0;
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

echo "release 148 applied: photo_locations, settings.locations_backfilled, settings.places_enabled"
