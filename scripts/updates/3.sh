#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Release 3 (issue #115): image groups (albums).
#
# Two new tables, nothing altered - see db.sql's image_groups comment for
# the shape and for why members are keyed by hash with no foreign key.
#
# Idempotent: both are IF NOT EXISTS, so a device that already has them
# (a fresh install, where db.sql created them) does nothing here.
#
# Run as root by scripts/update.sh, on the primary database and on every
# per-user database (issue #82), each of which carries its own schema.
set -euo pipefail

apply_to() {
    local db="$1"
    echo "applying schema to ${db}"
    mysql "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS image_groups (
  id VARCHAR(36) NOT NULL,
  name VARCHAR(150) NOT NULL,
  created DATETIME NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS image_group_files (
  group_id VARCHAR(36) NOT NULL,
  hash VARCHAR(64) NOT NULL,
  added DATETIME NOT NULL,
  PRIMARY KEY (group_id, hash),
  KEY (hash)
) ENGINE=InnoDB;
SQL
}

apply_to otc

for db in $(mysql -N -e "SELECT db_name FROM users" otc 2>/dev/null || true); do
    apply_to "$db"
done

echo "release 3 applied"
